// Package relayloop holds the end-to-end integration test that wires the real
// relay daemon (internal/relay) and the real launcher Link (internal/relaylink)
// together over a real tunnel, proving the two halves interoperate on the 0xF9
// protocol. Only pvpgn, WC3, and the joiner are faked (as echo servers).
package relayloop

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"wc3-launcher/internal/relay"
	"wc3-launcher/internal/relaylink"
)

// selfSignedTLS builds a throwaway server TLS config so the end-to-end test can
// exercise the real TLS-wrapped tunnel (the launcher dials with skip-verify).
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
		DNSNames:     []string{"localhost"},
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

// echoServer accepts many connections and echoes prefix+data back on each, so
// the test can prove which hop a byte traversed and that multiple WC3->pvpgn
// connections (native login opens several) each get their own socket.
func echoServer(t *testing.T, prefix string) (addr string, ln net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 512)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						_, _ = c.Write(append([]byte(prefix), buf[:n]...))
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), ln
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func readFullTimeout(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(r, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read timed out")
	}
	return buf
}

// TestRelayFullLoop: two WC3 realm connections -> tunnel -> relay -> pvpgn
// (echo), and joiner -> relay public port -> tunnel -> launcher -> WC3 (echo).
// Real Session and real Link, no protocol fakes. The second client stream is the
// regression guard for the multi-connection fix (native login's BNFTP download).
func TestRelayFullLoop(t *testing.T) {
	pvpgnAddr, pvpgnLn := echoServer(t, "PV:")
	defer pvpgnLn.Close()
	_, wc3Ln := echoServer(t, "WC:") // reached via gamePort, not the addr string
	defer wc3Ln.Close()
	gamePort := wc3Ln.Addr().(*net.TCPAddr).Port

	relayAddr := freePort(t)
	srv := &relay.Server{Listen: relayAddr, Pvpgn: pvpgnAddr, PublicIP: "127.0.0.1", Pool: relay.NewPool(6300, 6310), TLSConfig: selfSignedTLS(t)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.ListenAndServe(ctx) }()

	// Wait for the relay to accept tunnels.
	var link *relaylink.Link
	var port uint16
	var err error
	for i := 0; i < 50; i++ {
		link, port, err = relaylink.Dial(ctx, relayAddr, "token", gamePort, nil, &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Dial relay: %v", err)
	}
	defer link.GameOver()

	// First client stream (the realm connection): launcher -> relay -> pvpgn echo.
	bnet, err := link.OpenClientStream()
	if err != nil {
		t.Fatalf("OpenClientStream: %v", err)
	}
	if _, err := bnet.Write([]byte("LOGON")); err != nil {
		t.Fatal(err)
	}
	if got := readFullTimeout(t, bnet, len("PV:LOGON")); string(got) != "PV:LOGON" {
		t.Errorf("BNCS round trip = %q, want PV:LOGON", got)
	}

	// Second client stream (the BNFTP version-check download native login opens)
	// must get its OWN pvpgn connection, not be rejected -- the multi-connection fix.
	dl, err := link.OpenClientStream()
	if err != nil {
		t.Fatalf("second OpenClientStream: %v", err)
	}
	if _, err := dl.Write([]byte("GETMPQ")); err != nil {
		t.Fatal(err)
	}
	if got := readFullTimeout(t, dl, len("PV:GETMPQ")); string(got) != "PV:GETMPQ" {
		t.Errorf("second-stream round trip = %q, want PV:GETMPQ", got)
	}

	// Joiner hits the relay's advertised public port; bytes reach WC3 and back.
	joiner, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("joiner dial relay port %d: %v", port, err)
	}
	defer joiner.Close()
	if _, err := joiner.Write([]byte("JOIN-REQ")); err != nil {
		t.Fatal(err)
	}
	if got := readFullTimeout(t, joiner, len("WC:JOIN-REQ")); string(got) != "WC:JOIN-REQ" {
		t.Errorf("joiner round trip = %q, want WC:JOIN-REQ", got)
	}
}
