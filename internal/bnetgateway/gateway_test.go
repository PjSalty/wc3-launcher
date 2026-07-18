package bnetgateway

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

// TestProxyPassesBytesVerbatim runs the real listener: a WC3-side conn -> Proxy
// -> a fake upstream, and asserts the bytes arrive byte-for-byte and a reply
// flows back.
//
// The stream deliberately starts with a lone 0x01 protocol selector byte, which
// is what WC3 really sends first and is NOT part of the length-framed packet
// stream that follows. An earlier version of this proxy parsed the session as
// packets and desynced on exactly that byte ("bad magic 0x01"), so the login
// never completed. Leading with 0x01 here is the regression guard: if anyone
// reintroduces parsing, this test breaks.
func TestProxyPassesBytesVerbatim(t *testing.T) {
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upLn.Close()

	// The selector byte, then two framed packets, coalesced into one stream.
	sent := []byte{
		0x01,                               // protocol selector, not a packet
		0xFF, 0x45, 0x06, 0x00, 0xE0, 0x17, // SID_NETGAMEPORT, port 6112
		0xFF, 0x1C, 0x08, 0x00, 0x10, 0x00, 0x00, 0x00, // SID_STARTADVEX3
	}
	reply := []byte{0xFF, 0x25, 0x05, 0x00, 0xAA}

	got := make(chan []byte, 1)
	go func() {
		c, err := upLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, len(sent))
		if _, err := readFull(c, buf); err != nil {
			return
		}
		got <- buf
		_, _ = c.Write(reply)
		time.Sleep(50 * time.Millisecond) // let it flush before close
	}()

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyAddr := proxyLn.Addr().String()
	proxyLn.Close() // free it; Proxy re-binds the same addr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := &Proxy{Listen: proxyAddr, Upstream: upLn.Addr().String()}
	go func() { _ = p.ListenAndServe(ctx) }()

	var client net.Conn
	for i := 0; i < 50; i++ {
		if client, err = net.Dial("tcp", proxyAddr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if client == nil {
		t.Fatalf("could not reach proxy: %v", err)
	}
	defer client.Close()

	if _, err := client.Write(sent); err != nil {
		t.Fatal(err)
	}

	select {
	case g := <-got:
		if !bytes.Equal(g, sent) {
			t.Errorf("upstream saw altered bytes:\n got % x\nwant % x", g, sent)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the upstream to receive the stream")
	}

	back := make([]byte, len(reply))
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := readFull(client, back); err != nil {
		t.Fatalf("reading server->client reply: %v", err)
	}
	if !bytes.Equal(back, reply) {
		t.Errorf("reply = % x, want % x", back, reply)
	}
}

// TestProxyRequiresListenAndUpstream covers the two config guards.
func TestProxyRequiresListenAndUpstream(t *testing.T) {
	if err := (&Proxy{Upstream: "127.0.0.1:1"}).ListenAndServe(context.Background()); err == nil {
		t.Error("no Listen and no Listener: want an error, got nil")
	}
	if err := (&Proxy{Listen: "127.0.0.1:0"}).ListenAndServe(context.Background()); err == nil {
		t.Error("no Upstream and no Dial: want an error, got nil")
	}
}

// readFull fills buf, so a coalesced stream is compared whole rather than one
// short read at a time.
func readFull(c net.Conn, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := c.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
