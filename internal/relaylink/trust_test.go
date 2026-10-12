package relaylink

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wc3-launcher/internal/tunnel"
)

// fakeRelay serves the first TLS connection it accepts on host with fn and
// returns its address.
func fakeRelay(t *testing.T, host string, fn func(c net.Conn)) string {
	t.Helper()
	ln, err := tls.Listen("tcp", net.JoinHostPort(host, "0"), selfSignedTLS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		fn(c)
	}()
	return ln.Addr().String()
}

func trustAll() *tls.Config { return &tls.Config{InsecureSkipVerify: true} }

// quick is defaultTimings scaled down so liveness plays out in milliseconds.
func quick() timings {
	return timings{dial: 5 * time.Second, hello: 2 * time.Second, pingEvery: 50 * time.Millisecond, readIdle: 300 * time.Millisecond, write: 2 * time.Second}
}

// ack reads the launcher's HELLO and answers with port as the assigned game port.
func ack(c net.Conn, port int) error {
	if _, err := tunnel.ReadFrame(c); err != nil {
		return err
	}
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(port))
	return tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeHelloAck, Payload: b})
}

// freePort returns a loopback port nothing is listening on.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// up dials relayAddr with quick timings and ties the Link's teardown to the test.
func up(t *testing.T, relayAddr string) *Link {
	t.Helper()
	link, _, err := dial(context.Background(), relayAddr, "tok", 6119, nil, trustAll(), quick())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		link.GameOver()
		<-link.Done()
	})
	return link
}

// victim is an unrelated service on loopback. reached counts connections that
// delivered at least one byte, which the port probe's connect-and-close never does.
func victim(t *testing.T) (port int, reached *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	reached = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if n, _ := c.Read(make([]byte, 64)); n > 0 {
					reached.Add(1)
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, reached
}

// outwardIPv4 is an address of this machine that is not loopback, so a test relay
// on it looks to the launcher like a relay somewhere else.
func outwardIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 address here; TestCheckHostPort covers the decision without one")
	return ""
}

// The relay names the port joiners are bridged to on loopback. A relay that named
// a port some other local service is on got a two-way stream to that service.
func TestRemoteRelayCannotReachALoopbackService(t *testing.T) {
	port, reached := victim(t)
	addr := fakeRelay(t, outwardIPv4(t), func(c net.Conn) {
		if ack(c, port) != nil {
			return
		}
		_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeOpen, Stream: 2})
		_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeData, Stream: 2, Payload: []byte("CONFIG GET *\r\n")})
		_, _ = io.Copy(io.Discard, c)
	})

	link, _, err := dial(context.Background(), addr, "tok", 6119, nil, trustAll(), quick())
	if !errors.Is(err, ErrBadPort) {
		if link != nil {
			link.GameOver()
		}
		t.Fatalf("dial err = %v, want ErrBadPort", err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := reached.Load(); n != 0 {
		t.Fatalf("the loopback service received relay bytes on %d connection(s)", n)
	}
}

func TestCheckHostPort(t *testing.T) {
	busy, _ := victim(t)
	free := freePort(t)
	remote, local := net.ParseIP("203.0.113.5"), net.ParseIP("127.0.0.1")

	if err := checkHostPort(uint16(busy), remote); !errors.Is(err, ErrBadPort) {
		t.Errorf("a port a local service listens on: err = %v, want ErrBadPort", err)
	}
	if err := checkHostPort(uint16(free), remote); err != nil {
		t.Errorf("a free port: err = %v, want nil", err)
	}
	if err := checkHostPort(22, remote); !errors.Is(err, ErrBadPort) {
		t.Errorf("a system port: err = %v, want ErrBadPort", err)
	}
	if err := checkHostPort(22, local); !errors.Is(err, ErrBadPort) {
		t.Errorf("a system port from a local relay: err = %v, want ErrBadPort", err)
	}
	// A relay on this machine holds the port itself, so the probe is skipped.
	if err := checkHostPort(uint16(busy), local); err != nil {
		t.Errorf("a local relay's own port: err = %v, want nil", err)
	}
}

// Ctrl-C while waiting for HELLO_ACK did nothing: the wait had no deadline and
// did not watch the context.
func TestDialReturnsWhenCancelledDuringHello(t *testing.T) {
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) { _, _ = io.Copy(io.Discard, c) }) // never answers
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		_, _, err := Dial(ctx, addr, "tok", 6119, nil, trustAll())
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Dial err = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dial still blocked 3s after the context was cancelled")
	}
}

func TestDialGivesUpOnASilentRelay(t *testing.T) {
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) { _, _ = io.Copy(io.Discard, c) })
	tm := quick()
	tm.hello = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, _, err := dial(context.Background(), addr, "tok", 6119, nil, trustAll(), tm)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dial succeeded against a relay that never answered")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dial still blocked 3s in, with a 200ms HELLO deadline")
	}
}

// A refused token and a rejected certificate both used to surface as "relay
// unavailable". The caller needs to tell them from an unreachable relay.
func TestRefusalCarriesTheRelaysReason(t *testing.T) {
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if _, err := tunnel.ReadFrame(c); err != nil {
			return
		}
		_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeError, Payload: []byte("unauthorized")})
	})
	_, _, err := Dial(context.Background(), addr, "tok", 6119, nil, trustAll())
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != "unauthorized" {
		t.Fatalf("Dial err = %v, want a RefusedError with reason unauthorized", err)
	}
}

func TestRefusalReasonIsMadePrintable(t *testing.T) {
	if got := printable([]byte("full\x1b[2J\x00\n")); got != "full?[2J??" {
		t.Fatalf("printable = %q", got)
	}
	if got := printable(make([]byte, 500)); len(got) != 64 {
		t.Fatalf("printable kept %d bytes of 500, want 64", len(got))
	}
}

func TestRejectedCertificateIsUntrustedAndNothingIsSent(t *testing.T) {
	var gotHello atomic.Bool
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if _, err := tunnel.ReadFrame(c); err == nil {
			gotHello.Store(true)
		}
	})
	// System roots, no pin: the test relay's self-signed certificate fails.
	_, _, err := Dial(context.Background(), addr, "tok", 6119, nil, &tls.Config{ServerName: "127.0.0.1"})
	if !errors.Is(err, ErrUntrusted) {
		t.Fatalf("Dial err = %v, want ErrUntrusted", err)
	}
	time.Sleep(200 * time.Millisecond)
	if gotHello.Load() {
		t.Fatal("the token was sent to a relay whose certificate was rejected")
	}
}

// When the tunnel dropped, ServeHost kept the local gateway open: WC3's
// reconnects were accepted and dropped while the launcher said "ready to host".
func TestServeHostEndsWhenTheTunnelDrops(t *testing.T) {
	port := freePort(t)
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if ack(c, port) == nil {
			time.Sleep(200 * time.Millisecond) // then the relay goes away
		}
	})
	link := up(t, addr)
	gate, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- ServeHost(context.Background(), link, gate, nil) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrTunnelLost) {
			t.Fatalf("ServeHost err = %v, want ErrTunnelLost", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ServeHost still serving 3s after the relay closed the tunnel")
	}
	if c, err := net.DialTimeout("tcp", gate.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Fatal("the local gateway still accepts connections with no tunnel behind it")
	}
}

// A relay that stays connected but stops answering used to look alive forever.
func TestSilentTunnelIsNoticed(t *testing.T) {
	port := freePort(t)
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if ack(c, port) == nil {
			_, _ = io.Copy(io.Discard, c) // reads the pings, never answers
		}
	})
	link := up(t, addr)
	select {
	case <-link.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("link still up 3s in, with pings unanswered and a 300ms idle limit")
	}
}

func TestPingsKeepAQuietTunnelUp(t *testing.T) {
	port := freePort(t)
	var pings atomic.Int32
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if ack(c, port) != nil {
			return
		}
		for {
			f, err := tunnel.ReadFrame(c)
			if err != nil {
				return
			}
			if f.Type == tunnel.TypePing {
				pings.Add(1)
				if tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypePong}) != nil {
					return
				}
			}
		}
	})
	link := up(t, addr)

	select {
	case <-link.Done():
		t.Fatal("an idle tunnel whose pings are answered was torn down")
	case <-time.After(time.Second): // more than three idle limits
	}
	if n := pings.Load(); n < 5 {
		t.Fatalf("relay saw %d pings in 1s at a 50ms interval", n)
	}
}

// The relay opens even ids only. An OPEN on an odd or live id replaced that
// stream, and the launcher's own realm connection is stream 1.
func TestRelayCannotOpenOrReplaceALauncherStream(t *testing.T) {
	port := freePort(t)
	sent := make(chan struct{})
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if ack(c, port) != nil {
			return
		}
		for { // wait for the launcher's OPEN on stream 1, past any ping
			f, err := tunnel.ReadFrame(c)
			if err != nil {
				return
			}
			if f.Type == tunnel.TypeOpen {
				break
			}
		}
		for _, id := range []uint16{1, 3, 0} {
			_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeOpen, Stream: id})
		}
		_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeData, Stream: 1, Payload: []byte("still-mine")})
		close(sent)
		for {
			f, err := tunnel.ReadFrame(c)
			if err != nil {
				return
			}
			if f.Type == tunnel.TypePing && tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypePong}) != nil {
				return
			}
		}
	})
	link := up(t, addr)
	realm, err := link.OpenClientStream()
	if err != nil {
		t.Fatal(err)
	}
	mine := link.getStream(1)
	<-sent

	// The DATA frame was written after the three OPENs, so once it is readable
	// the launcher has already handled them. If stream 1 was replaced, the bytes
	// went to the replacement and this read never returns.
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, len("still-mine"))
		_, _ = io.ReadFull(realm, buf)
		got <- string(buf)
	}()
	select {
	case s := <-got:
		if s != "still-mine" {
			t.Fatalf("realm stream read %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the realm stream's data never arrived on it")
	}
	if link.getStream(1) != mine {
		t.Fatal("a relay OPEN on stream 1 replaced the launcher's realm stream")
	}
	if link.getStream(3) != nil || link.getStream(0) != nil {
		t.Fatal("the relay opened a stream on an id that is not its to open")
	}
}

func TestJoinerStreamsAreCapped(t *testing.T) {
	port := freePort(t)
	hosting := make(chan struct{})
	var refused atomic.Int32
	addr := fakeRelay(t, "127.0.0.1", func(c net.Conn) {
		if ack(c, port) != nil {
			return
		}
		<-hosting
		for i := 0; i < maxJoinerStreams+10; i++ {
			_ = tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypeOpen, Stream: uint16(2 + 2*i)})
		}
		for {
			f, err := tunnel.ReadFrame(c)
			if err != nil {
				return
			}
			switch f.Type {
			case tunnel.TypeClose:
				refused.Add(1)
			case tunnel.TypePing:
				if tunnel.WriteFrame(c, tunnel.Frame{Type: tunnel.TypePong}) != nil {
					return
				}
			}
		}
	})
	link := up(t, addr)
	link.SetHostPort(port)

	// WC3 starts hosting only now, after the port was vetted as free.
	wc3, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	var (
		joined atomic.Int32
		mu     sync.Mutex
		held   []net.Conn // every joiner stays connected until the test ends
	)
	defer func() {
		wc3.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	}()
	go func() {
		for {
			c, err := wc3.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
			joined.Add(1)
		}
	}()
	close(hosting)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && (refused.Load() < 10 || joined.Load() < maxJoinerStreams) {
		time.Sleep(20 * time.Millisecond)
	}
	if r, j := refused.Load(), joined.Load(); r != 10 || j != maxJoinerStreams {
		t.Fatalf("refused %d and bridged %d, want 10 refused and the cap of %d bridged", r, j, maxJoinerStreams)
	}
}
