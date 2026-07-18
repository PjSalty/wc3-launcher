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
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"wc3-launcher/internal/tunnel"
)

const copyBuf = 32 * 1024

// Link is one host game's tunnel to the relay.
type Link struct {
	tun      net.Conn
	gamePort int // 127.0.0.1:gamePort, WC3's local game listener
	logger   *log.Logger

	sendMu sync.Mutex // serializes frame writes on the shared tunnel

	mu           sync.Mutex
	streams      map[uint16]*stream
	nextClientID uint16 // next odd launcher-initiated (WC3 -> pvpgn) stream id
}

func (l *Link) logf(format string, args ...any) {
	if l.logger != nil {
		l.logger.Printf(format, args...)
	}
}

// Dial opens the tunnel, performs HELLO/HELLO_ACK, starts the demux loop, and
// returns the Link and the public port the relay allocated. The BnetGateway
// obtains its upstreams by calling OpenClientStream once per WC3 connection.
// token is the realm-auth token (opaque for the MVP). gamePort is where WC3
// hosts locally.
func Dial(ctx context.Context, relayAddr, token string, gamePort int, logger *log.Logger, tlsConf *tls.Config) (*Link, uint16, error) {
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
	d := &tls.Dialer{Config: tlsConf}
	tun, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, 0, fmt.Errorf("relaylink: dial relay %s (TLS): %w", relayAddr, err)
	}
	l := &Link{tun: tun, gamePort: gamePort, logger: logger, streams: make(map[uint16]*stream), nextClientID: tunnel.StreamFirstClient}

	if err := l.send(tunnel.Frame{Type: tunnel.TypeHello, Stream: tunnel.StreamControl, Payload: []byte(token)}); err != nil {
		tun.Close()
		return nil, 0, fmt.Errorf("relaylink: send HELLO: %w", err)
	}
	ack, err := tunnel.ReadFrame(tun)
	if err != nil {
		tun.Close()
		return nil, 0, fmt.Errorf("relaylink: read HELLO_ACK: %w", err)
	}
	if ack.Type == tunnel.TypeError {
		tun.Close()
		return nil, 0, fmt.Errorf("relaylink: relay refused: %s", ack.Payload)
	}
	if ack.Type != tunnel.TypeHelloAck || len(ack.Payload) < 2 {
		tun.Close()
		return nil, 0, fmt.Errorf("relaylink: bad HELLO_ACK (type %d)", ack.Type)
	}
	port := binary.LittleEndian.Uint16(ack.Payload[:2])

	go l.demux()
	l.logf("relay tunnel up: public port %d, WC3 realm+joiners multiplexed (joiners -> 127.0.0.1:%d)", port, gamePort)
	return l, port, nil
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
	l.mu.Lock()
	id := l.nextClientID
	l.nextClientID += 2
	l.mu.Unlock()
	s := l.newStream(id)
	if err := l.send(tunnel.Frame{Type: tunnel.TypeOpen, Stream: id}); err != nil {
		l.closeStream(id)
		return nil, err
	}
	l.logf("WC3 connection -> relay stream %d", id)
	return s, nil
}

// demux dispatches inbound frames: BNCS/joiner data to the matching stream, an
// OPEN to a new joiner bridge, plus close/ping.
func (l *Link) demux() {
	defer l.closeAll()
	for {
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
			go l.bridgeJoiner(l.newStream(f.Stream))
		case tunnel.TypeClose:
			l.closeStream(f.Stream)
		case tunnel.TypePing:
			if err := l.send(tunnel.Frame{Type: tunnel.TypePong, Stream: tunnel.StreamControl}); err != nil {
				return
			}
		}
	}
}

// bridgeJoiner dials WC3's local game listener and splices the joiner stream
// both ways: WC3 replies go up the stream, joiner bytes go to WC3.
func (l *Link) bridgeJoiner(s *stream) {
	defer l.closeStream(s.id)
	wc3, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.gamePort))
	if err != nil {
		l.logf("joiner stream %d: dial WC3 127.0.0.1:%d failed: %v", s.id, l.gamePort, err)
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

func (l *Link) send(f tunnel.Frame) error {
	l.sendMu.Lock()
	defer l.sendMu.Unlock()
	return tunnel.WriteFrame(l.tun, f)
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

func (l *Link) newStream(id uint16) *stream {
	s := newStreamState(id, l)
	l.mu.Lock()
	l.streams[id] = s
	l.mu.Unlock()
	return s
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
}
