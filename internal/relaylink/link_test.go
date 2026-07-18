package relaylink

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"math/big"
	"net"
	"testing"
	"time"

	"wc3-launcher/internal/tunnel"
)

// selfSignedTLS builds a throwaway server TLS config so tests can exercise the
// real TLS-wrapped tunnel (the launcher dials TLS-only).
func selfSignedTLS(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "wc3-relay-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
}

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
	got := 0
	for got < len(buf) {
		n, err := c.Read(buf[got:])
		got += n
		if err != nil {
			t.Fatalf("read %q: %v", want, err)
		}
	}
	if !bytes.Equal(buf, want) {
		t.Fatalf("read %q, want %q", buf, want)
	}
	_ = c.SetReadDeadline(time.Time{})
}

// TestLinkHostSide plays the relay side and proves the launcher Link: BNCS both
// ways on stream 1, and a joiner OPEN that dials the local WC3 listener and
// splices both ways.
func TestLinkHostSide(t *testing.T) {
	// Fake WC3 local game listener.
	wc3Ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wc3Ln.Close()
	gamePort := wc3Ln.Addr().(*net.TCPAddr).Port
	wc3Ch := make(chan net.Conn, 1)
	go func() {
		if c, err := wc3Ln.Accept(); err == nil {
			wc3Ch <- c
		}
	}()

	// Fake relay: accept the tunnel, HELLO -> HELLO_ACK{6200}, then hand it back.
	relayLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relayLn.Close()
	srvTLS := selfSignedTLS(t)
	tunCh := make(chan net.Conn, 1)
	go func() {
		rc, err := relayLn.Accept()
		if err != nil {
			return
		}
		tc := tls.Server(rc, srvTLS) // launcher dials TLS-only
		h, err := tunnel.ReadFrame(tc)
		if err != nil || h.Type != tunnel.TypeHello {
			return
		}
		ack := make([]byte, 2, 16)
		binary.LittleEndian.PutUint16(ack, 6200)
		ack = append(ack, []byte("203.0.113.10")...)
		_ = tunnel.WriteFrame(tc, tunnel.Frame{Type: tunnel.TypeHelloAck, Payload: ack})
		tunCh <- tc
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	link, port, err := Dial(ctx, relayLn.Addr().String(), "tok", gamePort, nil, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer link.GameOver()
	if port != 6200 {
		t.Fatalf("allocated port = %d, want 6200", port)
	}
	relayTun := <-tunCh

	// Opening the host realm stream tells the relay via OPEN{1} and yields the
	// upstream the BnetGateway writes to.
	bnet, err := link.OpenClientStream()
	if err != nil {
		t.Fatal(err)
	}
	if openF := readFrame(t, relayTun); openF.Type != tunnel.TypeOpen || openF.Stream != tunnel.StreamFirstClient {
		t.Fatalf("client OPEN = {t:%d s:%d}, want OPEN stream 1", openF.Type, openF.Stream)
	}

	// BNCS relay -> host (stream 1) reaches the bnet upstream.
	if err := tunnel.WriteFrame(relayTun, tunnel.Frame{Type: tunnel.TypeData, Stream: tunnel.StreamFirstClient, Payload: []byte("PVPGN-HELLO")}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("PVPGN-HELLO"))
	if _, err := readFullRWC(bnet, buf); err != nil {
		t.Fatalf("bnet read: %v", err)
	}
	if string(buf) != "PVPGN-HELLO" {
		t.Fatalf("bnet upstream got %q, want PVPGN-HELLO", buf)
	}

	// BNCS host -> relay (stream 1): writing to bnet emits DATA on stream 1.
	if _, err := bnet.Write([]byte("LOGON")); err != nil {
		t.Fatal(err)
	}
	up := readFrame(t, relayTun)
	if up.Type != tunnel.TypeData || up.Stream != tunnel.StreamFirstClient || string(up.Payload) != "LOGON" {
		t.Fatalf("BNCS up frame = {t:%d s:%d %q}", up.Type, up.Stream, up.Payload)
	}

	// A joiner OPEN makes the Link dial WC3.
	const js = tunnel.StreamFirstJoiner
	if err := tunnel.WriteFrame(relayTun, tunnel.Frame{Type: tunnel.TypeOpen, Stream: js}); err != nil {
		t.Fatal(err)
	}
	var wc3 net.Conn
	select {
	case wc3 = <-wc3Ch:
	case <-time.After(2 * time.Second):
		t.Fatal("Link never dialed the local WC3 listener on OPEN")
	}
	defer wc3.Close()

	// joiner -> WC3 over the stream reaches the WC3 conn.
	if err := tunnel.WriteFrame(relayTun, tunnel.Frame{Type: tunnel.TypeData, Stream: js, Payload: []byte("JOIN-REQ")}); err != nil {
		t.Fatal(err)
	}
	readN(t, wc3, []byte("JOIN-REQ"))

	// WC3 -> joiner comes back as DATA on the joiner stream.
	if _, err := wc3.Write([]byte("HOST-REPLY")); err != nil {
		t.Fatal(err)
	}
	jd := readFrame(t, relayTun)
	if jd.Type != tunnel.TypeData || jd.Stream != js || string(jd.Payload) != "HOST-REPLY" {
		t.Fatalf("joiner data frame = {t:%d s:%d %q}", jd.Type, jd.Stream, jd.Payload)
	}
}

func readFullRWC(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	got := 0
	for got < len(buf) {
		n, err := r.Read(buf[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
