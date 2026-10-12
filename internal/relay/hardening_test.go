package relay

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"wc3-launcher/internal/tunnel"
)

// countingBackend stands in for pvpgn and counts the connections it is handed.
type countingBackend struct {
	addr  string
	mu    sync.Mutex
	n     int
	conns []net.Conn
}

func newCountingBackend(t *testing.T) *countingBackend {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &countingBackend{addr: ln.Addr().String()}
	t.Cleanup(func() {
		ln.Close()
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, c := range b.conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b.mu.Lock()
			b.n++
			b.conns = append(b.conns, c)
			b.mu.Unlock()
		}
	}()
	return b
}

func (b *countingBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

// expect checks the backend was handed exactly want connections. The backend
// counts a connection when its accept loop gets to it, which lags the relay's
// dial, so the count is polled up to want and then given a moment to overshoot.
func (b *countingBackend) expect(t *testing.T, want int) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); b.count() < want && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := b.count(); n != want {
		t.Fatalf("backend saw %d connections, want %d", n, want)
	}
}

// startSession runs a Session over a TCP pair, completes HELLO, and returns the
// launcher's end, the allocated port, and a channel that closes when Run returns.
func startSession(t *testing.T, sess *Session, wrap func(net.Conn) net.Conn) (launcher net.Conn, port uint16, ended <-chan struct{}) {
	t.Helper()
	launcher, sessTun := tcpPair(t)
	t.Cleanup(func() { launcher.Close() })
	sess.tun = sessTun
	if wrap != nil {
		sess.tun = wrap(sessTun)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { defer close(done); _ = sess.Run(ctx) }()

	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeHello, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	ack := readFrame(t, launcher)
	if ack.Type != tunnel.TypeHelloAck || len(ack.Payload) < 2 {
		t.Fatalf("want HELLO_ACK, got type %d", ack.Type)
	}
	return launcher, uint16(ack.Payload[0]) | uint16(ack.Payload[1])<<8, done
}

func waitEnded(t *testing.T, ended <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(within):
		t.Fatalf("session still running after %v: %s", within, what)
	}
}

// The client-stream cap has to hold whatever id the tunnel picks. It counted odd
// ids only, so OPENs on even ids were dialed without limit: 200 of them made 200
// backend connections against a cap of 16.
func TestOpenOnEvenIDEndsSessionWithoutDialing(t *testing.T) {
	be := newCountingBackend(t)
	pool := NewPool(6250, 6260)
	launcher, _, ended := startSession(t, &Session{pvpgn: be.addr, pool: pool}, nil)

	for i := 0; i < 50; i++ {
		// Errors are expected once the relay hangs up; the assertions below decide.
		_ = tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeOpen, Stream: uint16(100 + 2*i)})
	}
	waitEnded(t, ended, 3*time.Second, "an OPEN on a joiner-half id must end the session")
	be.expect(t, 0)
	if got := pool.Available(); got != 11 {
		t.Fatalf("pool has %d free ports after the session ended, want all 11", got)
	}
}

func TestOddOpensStayUnderTheCap(t *testing.T) {
	be := newCountingBackend(t)
	launcher, _, _ := startSession(t, &Session{pvpgn: be.addr, pool: NewPool(6250, 6260)}, nil)

	const sent = maxClientStreams + 24
	for i := 0; i < sent; i++ {
		if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeOpen, Stream: uint16(1 + 2*i)}); err != nil {
			t.Fatal(err)
		}
	}
	// Each refused OPEN is answered with a CLOSE, so the refusals are the signal
	// that the relay has worked through everything that was sent.
	refused := 0
	for refused < sent-maxClientStreams {
		if f := readFrame(t, launcher); f.Type == tunnel.TypeClose {
			refused++
		}
	}
	be.expect(t, maxClientStreams)
}

// A second OPEN on a live id used to overwrite the map entry, leaving the first
// stream's socket and goroutines with nothing that would ever close them.
func TestOpenOnLiveIDEndsSession(t *testing.T) {
	be := newCountingBackend(t)
	launcher, _, ended := startSession(t, &Session{pvpgn: be.addr, pool: NewPool(6250, 6260)}, nil)

	for i := 0; i < 2; i++ {
		_ = tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypeOpen, Stream: tunnel.StreamFirstClient})
	}
	waitEnded(t, ended, 3*time.Second, "a second OPEN on a live id must end the session")
	be.expect(t, 1)
}

// A host that stops reading its tunnel blocked the first writer forever with the
// send mutex held, so the session never reached its idle deadline and kept its
// port. The write is bounded now, and a write that times out ends the session.
func TestHostThatStopsReadingLosesItsSession(t *testing.T) {
	pool := NewPool(6250, 6260)
	sess := &Session{pvpgn: "127.0.0.1:1", pool: pool, writeTimeout: 300 * time.Millisecond}
	_, port, ended := startSession(t, sess, nil) // the launcher end is never read again

	joiner, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { joiner.Close() })
	go func() {
		// Far more than the socket buffers between here and the unread launcher
		// end can hold, so the relay's tunnel write has to block.
		chunk := make([]byte, 64*1024)
		for i := 0; i < 2048; i++ {
			if _, err := joiner.Write(chunk); err != nil {
				return
			}
		}
	}()

	waitEnded(t, ended, 10*time.Second, "the blocked tunnel write must time out and end the session")
	if got := pool.Available(); got != 11 {
		t.Fatalf("pool has %d free ports after the session ended, want all 11", got)
	}
}

// boomConn panics on the first Read that completes after it is armed.
type boomConn struct {
	net.Conn
	armed atomic.Bool
}

func (b *boomConn) Read(p []byte) (int, error) {
	n, err := b.Conn.Read(p)
	if b.armed.Load() {
		panic("boom")
	}
	return n, err
}

// A panic in a session goroutine ends the whole process unless it is recovered
// there. Without the boundary this test does not fail, it takes the test binary
// down with it.
func TestPanicInSessionGoroutineIsContained(t *testing.T) {
	var bc *boomConn
	pool := NewPool(6250, 6260)
	launcher, _, ended := startSession(t, &Session{pvpgn: "127.0.0.1:1", pool: pool}, func(c net.Conn) net.Conn {
		bc = &boomConn{Conn: c}
		return bc
	})

	bc.armed.Store(true)
	if err := tunnel.WriteFrame(launcher, tunnel.Frame{Type: tunnel.TypePing}); err != nil {
		t.Fatal(err)
	}
	waitEnded(t, ended, 3*time.Second, "a panic in the tunnel reader must end that session")
	if got := pool.Available(); got != 11 {
		t.Fatalf("pool has %d free ports after the panic, want all 11", got)
	}
}

func TestCapKey(t *testing.T) {
	key := func(s string) string {
		a, err := net.ResolveTCPAddr("tcp", s)
		if err != nil {
			t.Fatalf("resolve %s: %v", s, err)
		}
		return capKey(a)
	}
	if got := key("203.0.113.7:51000"); got != "203.0.113.7" {
		t.Errorf("IPv4 keys on the address, got %q", got)
	}
	if got := key("[::ffff:203.0.113.7]:51000"); got != "203.0.113.7" {
		t.Errorf("an IPv4-mapped address keys like the IPv4 address, got %q", got)
	}
	a, b := key("[2001:db8:1:2::1]:51000"), key("[2001:db8:1:2:ffff:ffff:ffff:ffff]:51000")
	if a != b {
		t.Errorf("two addresses in one /64 must share a key: %q vs %q", a, b)
	}
	if c := key("[2001:db8:1:3::1]:51000"); c == a {
		t.Errorf("a different /64 must not share the key %q", c)
	}
}

// One IPv6 /64 could walk the whole global cap, because every address in it
// counted as a new source.
func TestOneV6PrefixCannotTakeEverySlot(t *testing.T) {
	srv := &Server{}
	admitted := 0
	for i := 0; i < maxSessionsTotal; i++ {
		addr := &net.TCPAddr{IP: net.ParseIP("2001:db8:1:2::").To16(), Port: 51000}
		addr.IP[14], addr.IP[15] = byte(i>>8), byte(i)
		if srv.admit(capKey(addr)) {
			admitted++
		}
	}
	if admitted != maxSessionsPerIP {
		t.Fatalf("one /64 was admitted %d times, want the per-source cap of %d", admitted, maxSessionsPerIP)
	}
}
