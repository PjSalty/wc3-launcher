package relay

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"wc3-launcher/internal/tunnel"
)

// readFrame reads one tunnel frame with a deadline so a broken muxer fails the
// test instead of hanging it.
func readFrame(t *testing.T, c net.Conn) tunnel.Frame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	f, err := tunnel.ReadFrame(c)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	return f
}

func readN(t *testing.T, c net.Conn, want []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, len(want))
	if _, err := readFull(c, buf); err != nil {
		t.Fatalf("read %q: %v", want, err)
	}
	if !bytes.Equal(buf, want) {
		t.Fatalf("read %q, want %q", buf, want)
	}
	_ = c.SetReadDeadline(time.Time{})
}

func readFull(c net.Conn, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := c.Read(buf[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}

func recvConn(t *testing.T, ch <-chan net.Conn, msg string) net.Conn {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
		return nil
	}
}

func tcpPair(t *testing.T) (client net.Conn, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	sc := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			sc <- c
		}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case server = <-sc:
	case <-time.After(2 * time.Second):
		t.Fatal("tcpPair: accept timeout")
	}
	return client, server
}

// TestSessionEndToEnd proves the muxer: pvpgn is dialed lazily on the first
// client OPEN, a SECOND client stream gets its own pvpgn socket (the
// multi-connection fix native login needs), and a joiner is fanned onto its own
// even stream both ways.
func TestSessionEndToEnd(t *testing.T) {
	// Fake pvpgn: accept multiple host connections (native login opens several).
	pvpgnLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pvpgnLn.Close()
	pvCh := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := pvpgnLn.Accept()
			if err != nil {
				return
			}
			pvCh <- c
		}
	}()

	launcher, sessTun := tcpPair(t)
	defer launcher.Close()

	sess := &Session{tun: sessTun, pvpgn: pvpgnLn.Addr().String(), pubIP: "203.0.113.10", pool: NewPool(6250, 6260)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = sess.Run(ctx) }()

	// HELLO -> HELLO_ACK{port}
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeHello, Payload: []byte("token")}); err != nil {
		t.Fatal(err)
	}
	ack := readFrame(t, launcher)
	if ack.Type != tunnel.TypeHelloAck || len(ack.Payload) < 2 {
		t.Fatalf("bad HELLO_ACK: type=%d len=%d", ack.Type, len(ack.Payload))
	}
	port := binary.LittleEndian.Uint16(ack.Payload[:2])
	if string(ack.Payload[2:]) != "203.0.113.10" {
		t.Errorf("ack public IP = %q, want 203.0.113.10", ack.Payload[2:])
	}

	// Opening the first client stream makes the relay dial pvpgn (lazy dial: no
	// pvpgn socket exists before the player connects).
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeOpen, Stream: tunnel.StreamFirstClient}); err != nil {
		t.Fatal(err)
	}
	pv := recvConn(t, pvCh, "pvpgn never got the first host connection")
	defer pv.Close()

	// BNCS host->pvpgn (stream 1)
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeData, Stream: tunnel.StreamFirstClient, Payload: []byte("BNET-UP")}); err != nil {
		t.Fatal(err)
	}
	readN(t, pv, []byte("BNET-UP"))

	// BNCS pvpgn->host (stream 1)
	if _, err := pv.Write([]byte("BNET-DOWN")); err != nil {
		t.Fatal(err)
	}
	down := readFrame(t, launcher)
	if down.Type != tunnel.TypeData || down.Stream != tunnel.StreamFirstClient || string(down.Payload) != "BNET-DOWN" {
		t.Fatalf("BNCS down frame = {t:%d s:%d %q}", down.Type, down.Stream, down.Payload)
	}

	// The multi-connection fix: a SECOND client stream (the BNFTP download native
	// login opens) must get its OWN pvpgn connection, not be rejected.
	const second = tunnel.StreamFirstClient + 2
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeOpen, Stream: second}); err != nil {
		t.Fatal(err)
	}
	pv2 := recvConn(t, pvCh, "pvpgn never got the second host connection")
	defer pv2.Close()
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeData, Stream: second, Payload: []byte("GET-MPQ")}); err != nil {
		t.Fatal(err)
	}
	readN(t, pv2, []byte("GET-MPQ"))

	// A joiner hits the public port -> OPEN{even stream}
	joiner, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer joiner.Close()
	open := readFrame(t, launcher)
	if open.Type != tunnel.TypeOpen || open.Stream < tunnel.StreamFirstJoiner || open.Stream%2 != 0 {
		t.Fatalf("expected OPEN with even joiner stream, got {t:%d s:%d}", open.Type, open.Stream)
	}
	js := open.Stream

	// joiner -> host over its stream
	if _, err := joiner.Write([]byte("JOIN-REQ")); err != nil {
		t.Fatal(err)
	}
	jd := readFrame(t, launcher)
	if jd.Type != tunnel.TypeData || jd.Stream != js || string(jd.Payload) != "JOIN-REQ" {
		t.Fatalf("joiner data frame = {t:%d s:%d %q}", jd.Type, jd.Stream, jd.Payload)
	}

	// host -> joiner over its stream
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeData, Stream: js, Payload: []byte("HOST-REPLY")}); err != nil {
		t.Fatal(err)
	}
	readN(t, joiner, []byte("HOST-REPLY"))
}
