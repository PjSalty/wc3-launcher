package relay

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"wc3-launcher/internal/tunnel"
)

// copyBuf sizes each socket read; WC3 lobby/in-game frames are small, so this
// is comfortable headroom.
const copyBuf = 32 * 1024

// helloTimeout bounds the pre-HELLO wait so a silent connection cannot park a
// goroutine + FD forever (slowloris). maxJoinersPerGame caps concurrent joiners
// on a game's public port (a real lobby is <=24) so a flood cannot exhaust the
// host launcher's dials/FDs.
const (
	helloTimeout      = 10 * time.Second
	maxJoinersPerGame = 32
	// maxClientStreams caps launcher-initiated (odd) pvpgn streams per session. A
	// native login opens only a handful (the realm connection plus a few BNFTP
	// downloads); the cap stops a tunnel from making the relay dial pvpgn without
	// bound (FD / pvpgn-connection exhaustion).
	maxClientStreams = 16
	// idleTimeout tears down a tunnel that goes silent after HELLO, so a dead or
	// malicious session cannot hold its pool port + session slot forever. Well
	// beyond WC3's realm/in-game keepalive cadence, so a live game never trips it.
	idleTimeout = 180 * time.Second
	// connOutCap bounds each stream's outbound queue. Deep enough for normal WC3
	// bursts, shallow enough that a stalled peer is dropped quickly.
	connOutCap = 512
	// tunWriteTimeout bounds one frame write to the host's tunnel. Every stream
	// shares that socket behind sendMu, so a host that stops reading would hold
	// the first blocked writer, the mutex, and with it the PONG that lets the read
	// loop get back to its idle deadline: the port and slot would never be freed.
	tunWriteTimeout = 20 * time.Second
	// streamWriteTimeout is the same bound for one pvpgn or joiner socket.
	streamWriteTimeout = 30 * time.Second
)

// relayConn is one stream's socket plus a bounded outbound queue. Writes from the
// single tunnel demux go through the queue, so a peer that stops reading (a
// stalled or hostile joiner) can never block the shared read loop and freeze every
// other stream on the session. Overflow drops only the offending stream.
type relayConn struct {
	net.Conn
	out       chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

// newRelayConn wraps c and starts its writer under run, the owning session's
// panic boundary (see Session.contain).
func newRelayConn(c net.Conn, run func(what string, fn func())) *relayConn {
	rc := &relayConn{Conn: c, out: make(chan []byte, connOutCap), closed: make(chan struct{})}
	go run("stream writer", rc.pumpOut)
	return rc
}

// pumpOut drains the queue to the socket; a write error or a write that does not
// finish in time kills just this stream.
func (rc *relayConn) pumpOut() {
	for {
		select {
		case b := <-rc.out:
			if err := rc.Conn.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil {
				rc.shutdown()
				return
			}
			if _, err := rc.Conn.Write(b); err != nil {
				rc.shutdown()
				return
			}
		case <-rc.closed:
			return
		}
	}
}

// deliver queues bytes for the peer and NEVER blocks the caller (the demux). A
// full queue means the peer is not keeping up, so drop this stream instead of
// stalling every other one. ReadFrame allocates a fresh payload per frame, so the
// buffer is safe to hand off without copying. Returns false if the stream is done.
func (rc *relayConn) deliver(b []byte) bool {
	select {
	case rc.out <- b:
		return true
	case <-rc.closed:
		return false
	default:
		rc.shutdown()
		return false
	}
}

func (rc *relayConn) shutdown() {
	rc.closeOnce.Do(func() {
		close(rc.closed)
		_ = rc.Conn.Close()
	})
}

// Session is one host player's relayed game: their outbound tunnel, a public
// listener on the allocated port for joiners, and the multiplexed streams. Each
// odd stream id is a WC3->pvpgn connection the launcher opened (a native login
// uses several: the realm connection plus BNFTP file transfers); each even id is
// a joiner that landed on the public port.
type Session struct {
	tun    net.Conn
	pvpgn  string
	pubIP  string
	pool   *Pool
	logger *log.Logger

	token       string // shared tunnel token launchers must present ("" = no check)
	requireAuth bool   // reject a wrong/absent token instead of just logging it

	port  uint16
	pubLn net.Listener

	sendMu       sync.Mutex    // serializes frame writes onto the shared tunnel
	writeTimeout time.Duration // per-frame tunnel write bound; 0 means tunWriteTimeout

	mu         sync.Mutex
	conns      map[uint16]*relayConn // live streams: odd = pvpgn dials, even = joiners
	nextJoiner uint16                // next even joiner stream id
	done       bool                  // torn down: no stream may be added any more

	cancel context.CancelFunc // ends the session; set in Run before any goroutine starts
}

func (s *Session) logf(format string, args ...any) {
	if s.logger != nil {
		s.logger.Printf(format, args...)
	}
}

// contain runs fn and keeps a panic inside this session: it is logged, the
// session is torn down, and every other session carries on. Every goroutine a
// session starts runs under it, because a panic in any goroutine ends the process.
func (s *Session) contain(what string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("session port %d: %s panicked, recovered: %v", s.port, what, r)
			if s.cancel != nil {
				s.cancel()
			}
		}
	}()
	fn()
}

// send writes one frame to the tunnel; the mutex keeps concurrent producers
// (every stream's pump) from interleaving on the wire. The write is bounded, and
// a failed one closes the tunnel: it leaves a partial frame on the wire, so the
// stream is desynced, and closing is what unblocks the read loop to end the
// session and free its port.
func (s *Session) send(f tunnel.Frame) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	wt := s.writeTimeout
	if wt == 0 {
		wt = tunWriteTimeout
	}
	err := s.tun.SetWriteDeadline(time.Now().Add(wt))
	if err == nil {
		err = tunnel.WriteFrame(s.tun, f)
	}
	if err != nil {
		_ = s.tun.Close()
	}
	return err
}

// sendData chunks b into DATA frames within the frame-size cap.
func (s *Session) sendData(stream uint16, b []byte) error {
	for len(b) > 0 {
		n := len(b)
		if n > tunnel.MaxPayload {
			n = tunnel.MaxPayload
		}
		if err := s.send(tunnel.Frame{Type: tunnel.TypeData, Stream: stream, Payload: b[:n]}); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// Run drives one session: HELLO, port alloc, public listener, then the tunnel
// read loop and joiner accept loop until either ends (which tears the rest
// down). pvpgn connections are dialed lazily, one per WC3 connection the
// launcher opens, so no pvpgn socket is held before the player actually connects.
func (s *Session) Run(ctx context.Context) error {
	s.conns = make(map[uint16]*relayConn)
	s.nextJoiner = tunnel.StreamFirstJoiner
	defer s.tun.Close()

	// The Server arms ONE pre-HELLO read deadline (covering the TLS/plain peek, the
	// TLS handshake, and this HELLO read) before handing the conn over, so a silent
	// connection cannot park a goroutine + port + FD. Cleared below once the session
	// is established, after which readTunnel enforces the rolling idle deadline.
	hello, err := tunnel.ReadFrame(s.tun)
	if err != nil {
		return fmt.Errorf("read HELLO: %w", err)
	}
	if hello.Type != tunnel.TypeHello {
		return fmt.Errorf("first frame type %d, want HELLO", hello.Type)
	}
	_ = s.tun.SetReadDeadline(time.Time{})

	// Tunnel auth: the launcher presents a shared token in HELLO. When a token is
	// configured and does not match, requireAuth rejects the tunnel; otherwise
	// (grace mode) it is logged and allowed, so already-distributed launchers keep
	// working during rollout. This is a shared secret baked into the launcher, so
	// it gates "must be running our launcher", not per-user identity - pvpgn's own
	// account login remains the real play-access auth.
	if s.token != "" && subtle.ConstantTimeCompare(hello.Payload, []byte(s.token)) != 1 {
		if s.requireAuth {
			_ = s.send(tunnel.Frame{Type: tunnel.TypeError, Payload: []byte("unauthorized")})
			return fmt.Errorf("tunnel from %s rejected: bad token", s.tun.RemoteAddr())
		}
		s.logf("tunnel from %s: token mismatch (grace mode, allowing)", s.tun.RemoteAddr())
	}

	port, err := s.pool.Alloc()
	if err != nil {
		_ = s.send(tunnel.Frame{Type: tunnel.TypeError, Payload: []byte("pool_full")})
		return err
	}
	s.port = port
	defer s.pool.Release(port)

	// Public listener on P for joiners, bound up front. SO_REUSEADDR/REUSEPORT
	// lets the first client stream's source-bound pvpgn dial (below) share P, so
	// pvpgn advertises the game at <reachable relay IP>:P.
	lc := net.ListenConfig{Control: reuseControl}
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return fmt.Errorf("bind public port %d: %w", port, err)
	}
	s.pubLn = ln
	defer ln.Close()

	ack := make([]byte, 2, 2+len(s.pubIP))
	binary.LittleEndian.PutUint16(ack, port)
	ack = append(ack, []byte(s.pubIP)...)
	if err := s.send(tunnel.Frame{Type: tunnel.TypeHelloAck, Payload: ack}); err != nil {
		return err
	}
	s.logf("session up: port %d -> host tunnel, pvpgn %s", port, s.pvpgn)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.cancel = cancel
	// Cancelling closes the sockets so blocked reads/accepts unblock.
	go s.contain("teardown", func() {
		<-ctx.Done()
		_ = ln.Close()
		_ = s.tun.Close()
		s.closeAllConns()
	})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); defer cancel(); s.contain("tunnel reader", func() { s.readTunnel(ctx) }) }()
	go func() { defer wg.Done(); defer cancel(); s.contain("joiner accept", s.acceptJoiners) }()
	wg.Wait()
	s.logf("session down: port %d", port)
	return nil
}

// readTunnel dispatches inbound frames: OPEN dials a fresh pvpgn connection for
// the launcher's new WC3 socket, DATA goes to the matching stream, plus
// close/ping/gameover control.
func (s *Session) readTunnel(ctx context.Context) {
	for {
		// Rolling idle deadline: a tunnel that goes silent after HELLO must not hold
		// its pool port + session slot indefinitely. Re-armed on every frame.
		_ = s.tun.SetReadDeadline(time.Now().Add(idleTimeout))
		f, err := tunnel.ReadFrame(s.tun)
		if err != nil {
			return
		}
		switch f.Type {
		case tunnel.TypeOpen:
			// The launcher may open odd ids only, and only free ones. An even id
			// would land in the joiner half of the map, outside the client cap, and
			// a live id would orphan the stream already on it. A real launcher
			// sends neither, so the session ends here.
			if !s.launcherMayOpen(f.Stream) {
				s.logf("session port %d: OPEN on stream %d is not the launcher's to open, ending session", s.port, f.Stream)
				return
			}
			// Dial synchronously so conns[id] exists before this stream's DATA
			// frames (which follow immediately in the ordered tunnel) arrive.
			s.openPvpgn(ctx, f.Stream)
		case tunnel.TypeData:
			s.mu.Lock()
			c := s.conns[f.Stream]
			s.mu.Unlock()
			if c != nil && !c.deliver(f.Payload) {
				s.closeConn(f.Stream)
			}
		case tunnel.TypeClose:
			s.closeConn(f.Stream)
		case tunnel.TypePing:
			if err := s.send(tunnel.Frame{Type: tunnel.TypePong, Stream: tunnel.StreamControl}); err != nil {
				return
			}
		case tunnel.TypeGameOver:
			return
		}
	}
}

// launcherMayOpen reports whether id is one the launcher is allowed to open: an
// odd id with no live stream on it.
func (s *Session) launcherMayOpen(id uint16) bool {
	if id%2 == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, live := s.conns[id]
	return !live
}

// addConn registers a stream, unless the session is already torn down, in which
// case nothing would ever close it: a dial that outlives the session must not
// leave its socket behind.
func (s *Session) addConn(id uint16, rc *relayConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return false
	}
	s.conns[id] = rc
	return true
}

// openPvpgn dials pvpgn for a launcher-opened WC3 connection. The source port is
// NOT bound: pvpgn advertises the game at <source IP>:<WC3's declared
// netgameport>, and the source IP is the relay's regardless of source port, so
// the launcher declaring netgameport = P is what pins the address. Binding the
// source to P would only make this socket share P with the public joiner listener
// under SO_REUSEPORT, for no benefit. Dial from an ephemeral source.
func (s *Session) openPvpgn(ctx context.Context, id uint16) {
	// Cap launcher-initiated (odd) streams: a native login opens only a handful of
	// pvpgn sockets, so refusing past the cap stops a tunnel from making the relay
	// dial pvpgn without bound (FD / pvpgn-connection exhaustion).
	s.mu.Lock()
	clients := 0
	for cid := range s.conns {
		if cid%2 == 1 {
			clients++
		}
	}
	s.mu.Unlock()
	if clients >= maxClientStreams {
		s.logf("stream %d refused: %d client streams already open (cap %d)", id, clients, maxClientStreams)
		_ = s.send(tunnel.Frame{Type: tunnel.TypeClose, Stream: id})
		return
	}

	// Bound each pvpgn dial so a hung backend stalls only this stream's open, not
	// the session read loop that calls openPvpgn synchronously.
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(dctx, "tcp", s.pvpgn)
	if err != nil {
		s.logf("stream %d: dial pvpgn %s failed: %v", id, s.pvpgn, err)
		_ = s.send(tunnel.Frame{Type: tunnel.TypeClose, Stream: id})
		return
	}
	rc := newRelayConn(conn, s.contain)
	if !s.addConn(id, rc) {
		rc.shutdown()
		return
	}
	s.logf("stream %d -> pvpgn %s", id, s.pvpgn)
	go s.contain("stream pump", func() { s.pump(id, rc) })
}

// acceptJoiners assigns an even stream to each inbound joiner, tells the host via
// OPEN, and pumps that joiner's bytes over its stream.
func (s *Session) acceptJoiners() {
	for {
		jc, err := s.pubLn.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.done {
			s.mu.Unlock()
			_ = jc.Close()
			return
		}
		// Cap concurrent joiners: a real WC3 lobby is <=24, so refusing past the
		// cap stops a flood on the public port from exhausting host-side FDs while
		// never turning away a legitimate joiner.
		live := 0
		for id := range s.conns {
			if id >= tunnel.StreamFirstJoiner && id%2 == 0 {
				live++
			}
		}
		if live >= maxJoinersPerGame {
			s.mu.Unlock()
			s.logf("joiner %s refused on port %d: %d joiners already live", jc.RemoteAddr(), s.port, live)
			_ = jc.Close()
			continue
		}
		// Allocate the next FREE even joiner id. A bare monotonic counter can wrap
		// (uint16) back onto a still-live stream and collide, so skip taken ids and
		// reset past 0 (control) and odd (launcher) ids. The joiner cap above bounds
		// this search to a few probes.
		id := s.nextJoiner
		for i := 0; i <= maxJoinersPerGame+2; i++ {
			if id < tunnel.StreamFirstJoiner {
				id = tunnel.StreamFirstJoiner
			}
			if _, taken := s.conns[id]; !taken {
				break
			}
			id += 2
		}
		s.nextJoiner = id + 2
		rc := newRelayConn(jc, s.contain)
		s.conns[id] = rc
		s.mu.Unlock()
		if err := s.send(tunnel.Frame{Type: tunnel.TypeOpen, Stream: id}); err != nil {
			s.closeConn(id)
			return
		}
		s.logf("joiner %s -> stream %d on port %d", jc.RemoteAddr(), id, s.port)
		go s.contain("stream pump", func() { s.pump(id, rc) })
	}
}

// pump forwards one stream's socket -> the host over its stream id, and notifies
// the host with CLOSE when it ends. Used for both pvpgn connections and joiners.
func (s *Session) pump(id uint16, c *relayConn) {
	buf := make([]byte, copyBuf)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if serr := s.sendData(id, buf[:n]); serr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = s.send(tunnel.Frame{Type: tunnel.TypeClose, Stream: id})
	s.closeConn(id)
}

func (s *Session) closeConn(id uint16) {
	s.mu.Lock()
	c := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if c != nil {
		c.shutdown()
	}
}

func (s *Session) closeAllConns() {
	s.mu.Lock()
	s.done = true
	all := make([]*relayConn, 0, len(s.conns))
	for id, c := range s.conns {
		all = append(all, c)
		delete(s.conns, id)
	}
	s.mu.Unlock()
	for _, c := range all {
		c.shutdown()
	}
}
