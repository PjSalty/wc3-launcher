// Package relaylink is the launcher (host player) side of the native-host
// relay. It opens one OUTBOUND tunnel to the relay daemon (zero inbound, zero
// router config), rides the host's realm connection over BNCS stream 1 so
// PvPGN advertises the relay's IP as the game host, and on each joiner OPEN it
// dials WC3's local game listener and splices the joiner's bytes to it.
//
// It is the counterpart of internal/relay.Session: that side allocates the public
// port and accepts joiners; this side dials WC3 and consumes joiner streams.
package relaylink

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"wc3-launcher/internal/tunnel"
)

const copyBuf = 32 * 1024

// timings are the tunnel's clocks. Dial uses defaultTimings; the tests pass
// shorter ones to dial.
type timings struct {
	// dial bounds the TCP connect plus TLS handshake, hello the HELLO exchange
	// after it. A relay that accepts and then says nothing would otherwise hang
	// the launcher before the game has even started.
	dial, hello time.Duration
	// pingEvery and readIdle are the liveness check. The relay answers every
	// PING, so a tunnel that has produced no frame for readIdle is dead, not
	// quiet. The pings also keep the relay's own idle timer fed while the host
	// sits in a lobby.
	pingEvery, readIdle time.Duration
	// write bounds one frame write, so a relay that stops reading cannot block
	// every stream behind the send mutex.
	write time.Duration
}

func defaultTimings() timings {
	return timings{
		dial:      15 * time.Second,
		hello:     10 * time.Second,
		pingEvery: 30 * time.Second,
		readIdle:  90 * time.Second,
		write:     20 * time.Second,
	}
}

// maxJoinerStreams caps the joiner streams the relay may hold open at once. The
// relay's own cap is 32; this is the launcher not taking its word for it.
const maxJoinerStreams = 64

// What Dial and ServeHost can fail with that is NOT "the relay could not be
// reached". A caller that falls back to a direct connection has to tell these
// apart: the first one means the peer is not the server it claims to be.
var (
	// ErrUntrusted means the relay's certificate was rejected, by the pin or by
	// chain verification. Nothing was sent to it.
	ErrUntrusted = errors.New("relay certificate rejected")
	// ErrBadPort means the relay assigned a host port the launcher will not
	// bridge joiners to.
	ErrBadPort = errors.New("relay assigned an unusable port")
	// ErrTunnelLost means an established tunnel dropped.
	ErrTunnelLost = errors.New("relay tunnel lost")
)

// RefusedError is the relay answering HELLO with an error frame. Reason is the
// relay's code: "unauthorized" for a wrong token, "pool_full" when it has no
// port left.
type RefusedError struct{ Reason string }

func (e *RefusedError) Error() string { return "relay refused the tunnel: " + e.Reason }

// Link is one host game's tunnel to the relay.
type Link struct {
	tun    net.Conn
	logger *log.Logger
	t      timings

	sendMu sync.Mutex // serializes frame writes on the shared tunnel

	mu           sync.Mutex
	gamePort     int // 127.0.0.1:gamePort, WC3's local game listener
	streams      map[uint16]*stream
	nextClientID uint16 // next odd launcher-initiated (WC3 -> pvpgn) stream id

	done     chan struct{} // closed once the tunnel is gone
	doneOnce sync.Once
}

func (l *Link) logf(format string, args ...any) {
	if l.logger != nil {
		l.logger.Printf(format, args...)
	}
}

// Done is closed when the tunnel is gone, whoever ended it.
func (l *Link) Done() <-chan struct{} { return l.done }

// Dial opens the tunnel, performs HELLO/HELLO_ACK, starts the demux loop, and
// returns the Link and the public port the relay allocated. The BnetGateway
// obtains its upstreams by calling OpenClientStream once per WC3 connection.
// token is the realm-auth token (opaque for the MVP). gamePort is where WC3
// hosts locally.
func Dial(ctx context.Context, relayAddr, token string, gamePort int, logger *log.Logger, tlsConf *tls.Config) (*Link, uint16, error) {
	return dial(ctx, relayAddr, token, gamePort, logger, tlsConf, defaultTimings())
}

func dial(ctx context.Context, relayAddr, token string, gamePort int, logger *log.Logger, tlsConf *tls.Config, t timings) (*Link, uint16, error) {
	host, _, err := net.SplitHostPort(relayAddr)
	if err != nil {
		host = relayAddr
	}
	// TLS only: the tunnel carries the realm session end to end, so it must be
	// encrypted. The default config verifies the relay's certificate for its
	// hostname against the system roots; there is deliberately no plaintext
	// fallback. tlsConf is non-nil only in tests (self-signed cert).
	if tlsConf == nil {
		tlsConf = &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	}
	dctx, cancel := context.WithTimeout(ctx, t.dial)
	defer cancel()
	d := &tls.Dialer{Config: tlsConf}
	tun, err := d.DialContext(dctx, "tcp", relayAddr)
	if err != nil {
		var cve *tls.CertificateVerificationError
		if errors.As(err, &cve) && !errors.Is(err, ErrUntrusted) {
			return nil, 0, fmt.Errorf("relaylink: %w: %v", ErrUntrusted, err)
		}
		return nil, 0, fmt.Errorf("relaylink: dial relay %s (TLS): %w", relayAddr, err)
	}
	l := &Link{
		tun:          tun,
		t:            t,
		gamePort:     gamePort,
		logger:       logger,
		streams:      make(map[uint16]*stream),
		nextClientID: tunnel.StreamFirstClient,
		done:         make(chan struct{}),
	}

	// The dial honoured ctx. The frame exchange has to as well, or Ctrl-C here
	// does nothing until the relay decides to speak.
	stop := context.AfterFunc(ctx, func() { _ = tun.Close() })
	defer stop()
	fail := func(err error) (*Link, uint16, error) {
		_ = tun.Close()
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, err
	}
	if err := tun.SetDeadline(time.Now().Add(t.hello)); err != nil {
		return fail(fmt.Errorf("relaylink: arm HELLO deadline: %w", err))
	}
	if err := l.send(tunnel.Frame{Type: tunnel.TypeHello, Stream: tunnel.StreamControl, Payload: []byte(token)}); err != nil {
		return fail(fmt.Errorf("relaylink: send HELLO: %w", err))
	}
	ack, err := tunnel.ReadFrame(tun)
	if err != nil {
		return fail(fmt.Errorf("relaylink: read HELLO_ACK: %w", err))
	}
	if ack.Type == tunnel.TypeError {
		return fail(&RefusedError{Reason: printable(ack.Payload)})
	}
	if ack.Type != tunnel.TypeHelloAck || len(ack.Payload) < 2 {
		return fail(fmt.Errorf("relaylink: bad HELLO_ACK (type %d)", ack.Type))
	}
	port := binary.LittleEndian.Uint16(ack.Payload[:2])
	if err := checkHostPort(port, remoteIP(tun)); err != nil {
		return fail(err)
	}
	if err := tun.SetDeadline(time.Time{}); err != nil {
		return fail(fmt.Errorf("relaylink: clear HELLO deadline: %w", err))
	}
	if !stop() {
		return fail(ctx.Err()) // ctx fired while we finished: the tunnel is closed
	}

	go l.demux()
	go l.keepalive()
	l.logf("relay tunnel up: public port %d, WC3 realm+joiners multiplexed (joiners -> 127.0.0.1:%d)", port, gamePort)
	return l, port, nil
}

// checkHostPort vets the port the relay assigned. WC3 will host on it, and every
// stream the relay opens is connected to it on loopback, so a relay that named a
// port some other local service is on would be handed a connection to that
// service. In a real session nothing listens there yet: WC3 binds it only when
// the player hosts a game.
//
// A relay reached over loopback is this machine. It holds the port itself, so
// the probe would always find it, and it can already reach every loopback
// service without the launcher's help, so there is nothing to protect.
func checkHostPort(port uint16, relay net.IP) error {
	if port < 1024 {
		return fmt.Errorf("%w: %d is a system port", ErrBadPort, port)
	}
	if relay != nil && relay.IsLoopback() {
		return nil
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		return nil // nothing answers there, which is what a free port looks like
	}
	_ = c.Close()
	return fmt.Errorf("%w: another program on this computer is already listening on port %d", ErrBadPort, port)
}

func remoteIP(c net.Conn) net.IP {
	if a, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}

// printable keeps a relay-supplied string safe to show: it is the other side's
// bytes, headed for the player's terminal.
func printable(b []byte) string {
	if len(b) > 64 {
		b = b[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, string(b))
}

// SetHostPort points the joiner bridge at the port WC3 actually hosts its game
// on. In the relay flow that MUST be the relay's allocated pool port P: pvpgn
// advertises the game at <relay IP>:<WC3's declared netgameport>, so WC3 has to
// declare and host on P for joiners to land on the relay's public port P and be
// spliced back to WC3. Call before ServeHost (before any joiner can arrive).
func (l *Link) SetHostPort(p int) {
	l.mu.Lock()
	l.gamePort = p
	l.mu.Unlock()
}

// OpenClientStream registers a new launcher-initiated stream for one WC3
// connection to the local gateway and tells the relay to dial pvpgn for it. WC3
// opens several such connections during a native login (the realm connection
// plus BNFTP version-check downloads); each gets its own pvpgn socket. Odd ids
// keep these from colliding with the relay's even joiner ids.
func (l *Link) OpenClientStream() (io.ReadWriteCloser, error) {
	select {
	case <-l.done:
		return nil, ErrTunnelLost
	default:
	}
	l.mu.Lock()
	// Skip ids still in use: the counter wraps after 32768 opens, and the relay
	// ends a session that opens a live id.
	id := l.nextClientID
	for i := 0; i < 1<<15; i++ {
		if _, live := l.streams[id]; !live {
			break
		}
		id += 2
	}
	l.nextClientID = id + 2
	s := newStreamState(id, l)
	l.streams[id] = s
	l.mu.Unlock()
	if err := l.send(tunnel.Frame{Type: tunnel.TypeOpen, Stream: id}); err != nil {
		l.closeStream(id)
		return nil, err
	}
	l.logf("WC3 connection -> relay stream %d", id)
	return s, nil
}

// keepalive pings the relay until the tunnel is gone. Each PONG is what feeds
// demux's read deadline on an otherwise idle tunnel.
func (l *Link) keepalive() {
	tick := time.NewTicker(l.t.pingEvery)
	defer tick.Stop()
	for {
		select {
		case <-l.done:
			return
		case <-tick.C:
			if err := l.send(tunnel.Frame{Type: tunnel.TypePing, Stream: tunnel.StreamControl}); err != nil {
				return
			}
		}
	}
}

// demux dispatches inbound frames: BNCS/joiner data to the matching stream, an
// OPEN to a new joiner bridge, plus close/ping. It ends, and takes the Link with
// it, when the tunnel errors or stays silent past readIdle.
func (l *Link) demux() {
	defer l.closeAll()
	for {
		if err := l.tun.SetReadDeadline(time.Now().Add(l.t.readIdle)); err != nil {
			return
		}
		f, err := tunnel.ReadFrame(l.tun)
		if err != nil {
			return
		}
		switch f.Type {
		case tunnel.TypeData:
			if s := l.getStream(f.Stream); s != nil {
				s.deliver(f.Payload)
			}
		case tunnel.TypeOpen:
			if s := l.acceptJoiner(f.Stream); s != nil {
				go l.bridgeJoiner(s)
			}
		case tunnel.TypeClose:
			l.closeStream(f.Stream)
		case tunnel.TypePing:
			if err := l.send(tunnel.Frame{Type: tunnel.TypePong, Stream: tunnel.StreamControl}); err != nil {
				return
			}
		}
	}
}

// acceptJoiner registers a relay-opened stream, or returns nil. The relay may
// open even ids only, free ones, and only so many at once. Without that a relay
// could replace a live stream, the realm connection included, or make the
// launcher dial WC3 without bound.
func (l *Link) acceptJoiner(id uint16) *stream {
	l.mu.Lock()
	if id < tunnel.StreamFirstJoiner || id%2 != 0 || l.streams[id] != nil {
		l.mu.Unlock()
		l.logf("ignoring OPEN on stream %d: not a free joiner id", id)
		return nil
	}
	joiners := 0
	for sid := range l.streams {
		if sid%2 == 0 {
			joiners++
		}
	}
	if joiners >= maxJoinerStreams {
		l.mu.Unlock()
		l.logf("refusing joiner stream %d: %d already open", id, joiners)
		_ = l.send(tunnel.Frame{Type: tunnel.TypeClose, Stream: id})
		return nil
	}
	s := newStreamState(id, l)
	l.streams[id] = s
	l.mu.Unlock()
	return s
}

// bridgeJoiner dials WC3's local game listener and splices the joiner stream
// both ways: WC3 replies go up the stream, joiner bytes go to WC3.
func (l *Link) bridgeJoiner(s *stream) {
	defer l.closeStream(s.id)
	l.mu.Lock()
	gamePort := l.gamePort
	l.mu.Unlock()
	wc3, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", gamePort))
	if err != nil {
		l.logf("joiner stream %d: dial WC3 127.0.0.1:%d failed: %v", s.id, gamePort, err)
		s.Close() // tell the relay to drop the joiner it opened
		return
	}
	defer wc3.Close()

	go func() {
		buf := make([]byte, copyBuf)
		for {
			n, err := wc3.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		s.Close() // WC3 side ended: CLOSE the stream (tells the relay + unblocks the copy below)
	}()
	// stream -> WC3 until the joiner stream ends.
	_, _ = io.Copy(wc3, s)
	s.Close() // ensure the relay is told and the WC3 side is released
}

// GameOver tells the relay to release the port and tear the session down.
func (l *Link) GameOver() {
	_ = l.send(tunnel.Frame{Type: tunnel.TypeGameOver, Stream: tunnel.StreamControl})
	l.tun.Close()
}

// send writes one frame. The write is bounded, and a failed one closes the
// tunnel: it leaves a partial frame on the wire, so everything after it would be
// misread, and closing is what ends demux and reports the loss.
func (l *Link) send(f tunnel.Frame) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	err := l.tun.SetWriteDeadline(time.Now().Add(l.t.write))
	if err == nil {
		err = tunnel.WriteFrame(l.tun, f)
	}
	if err != nil {
		_ = l.tun.Close()
	}
	return err
}

func (l *Link) sendData(stream uint16, b []byte) error {
	for len(b) > 0 {
		n := len(b)
		if n > tunnel.MaxPayload {
			n = tunnel.MaxPayload
		}
		if err := l.send(tunnel.Frame{Type: tunnel.TypeData, Stream: stream, Payload: b[:n]}); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (l *Link) getStream(id uint16) *stream {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.streams[id]
}

func (l *Link) closeStream(id uint16) {
	l.mu.Lock()
	s := l.streams[id]
	delete(l.streams, id)
	l.mu.Unlock()
	if s != nil {
		s.closeIncoming()
	}
}

func (l *Link) closeAll() {
	l.mu.Lock()
	all := make([]*stream, 0, len(l.streams))
	for id, s := range l.streams {
		all = append(all, s)
		delete(l.streams, id)
	}
	l.mu.Unlock()
	for _, s := range all {
		s.closeIncoming()
	}
	l.tun.Close()
	l.doneOnce.Do(func() { close(l.done) })
}
