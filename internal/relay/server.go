package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// Concurrency caps for the public tunnel port. A real deployment has a handful
// of hosts; these bound resource use so a flood on the tunnel port cannot
// exhaust the port pool, FDs, or goroutines.
//
// These sit UNDER the token gate and TLS, and are deliberately independent of
// them: they apply before a session proves anything, so an attacker who never
// sends a valid token still cannot make the relay allocate without bound. Auth
// decides who gets in; these decide how much anyone can take.
const (
	maxSessionsPerIP = 4
	maxSessionsTotal = 80
)

// Server accepts host launcher tunnels and runs a Session per connection.
type Server struct {
	Listen   string // tunnel listen addr, e.g. ":7000"
	Pvpgn    string // pvpgn bnet addr, e.g. "127.0.0.1:6112"
	PublicIP string // public IP advertised to joiners
	Pool     *Pool
	Logger   *log.Logger

	Token       string // shared tunnel token launchers must present ("" = no check)
	RequireAuth bool   // reject a wrong/absent token instead of just logging it

	TLSConfig  *tls.Config // if set, TLS-handshake connections are terminated here
	RequireTLS bool        // reject plaintext tunnels (TLS-only end state)

	mu    sync.Mutex
	perIP map[string]int
	total int
}

func (srv *Server) logf(format string, args ...any) {
	if srv.Logger != nil {
		srv.Logger.Printf(format, args...)
	}
}

// admit reserves a session slot for ip, or returns false if a per-IP or global
// cap is already hit.
func (srv *Server) admit(ip string) bool {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.perIP == nil {
		srv.perIP = make(map[string]int)
	}
	if srv.total >= maxSessionsTotal || srv.perIP[ip] >= maxSessionsPerIP {
		return false
	}
	srv.perIP[ip]++
	srv.total++
	return true
}

func (srv *Server) release(ip string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.perIP[ip] > 0 {
		srv.perIP[ip]--
		if srv.perIP[ip] == 0 {
			delete(srv.perIP, ip)
		}
	}
	if srv.total > 0 {
		srv.total--
	}
}

// ListenAndServe accepts tunnels until ctx is cancelled or Accept fails.
func (srv *Server) ListenAndServe(ctx context.Context) error {
	if srv.Pool == nil {
		return fmt.Errorf("relay: nil Pool")
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", srv.Listen)
	if err != nil {
		return fmt.Errorf("relay: listen %s: %w", srv.Listen, err)
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	srv.logf("relay listening for host tunnels on %s (pvpgn %s, %d ports free)",
		srv.Listen, srv.Pvpgn, srv.Pool.Available())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("relay: accept: %w", err)
		}
		ip := hostOnly(conn.RemoteAddr())
		if !srv.admit(ip) {
			srv.logf("tunnel from %s rejected: session cap reached", conn.RemoteAddr())
			_ = conn.Close()
			continue
		}
		go func(raw net.Conn, ip string) {
			// A panic in one session must never take the whole internet-facing
			// daemon down (and every other live game) with it. Contain it here so
			// a single bad session dies alone. The slot is released first (LIFO),
			// so a panic never leaks a session slot either.
			defer func() {
				if r := recover(); r != nil {
					srv.logf("session from %s panicked, recovered: %v", raw.RemoteAddr(), r)
					_ = raw.Close()
				}
			}()
			defer srv.release(ip)
			// ONE absolute pre-HELLO budget covering the TLS/plain peek, the TLS
			// handshake, and the HELLO frame. Session.Run clears it once the session
			// is established, so a slowloris gets helloTimeout total, not per-step.
			_ = raw.SetReadDeadline(time.Now().Add(helloTimeout))
			tun, ok := srv.wrapTLS(raw)
			if !ok {
				_ = raw.Close()
				return
			}
			s := &Session{tun: tun, pvpgn: srv.Pvpgn, pubIP: srv.PublicIP, pool: srv.Pool, logger: srv.Logger, token: srv.Token, requireAuth: srv.RequireAuth}
			if err := s.Run(ctx); err != nil && ctx.Err() == nil {
				srv.logf("session from %s ended: %v", raw.RemoteAddr(), err)
			}
		}(conn, ip)
	}
}

// wrapTLS peeks the first byte to tell a TLS handshake (0x16) from the plain
// tunnel magic (0xF9). TLS connections are terminated here; a plaintext tunnel
// is accepted only while RequireTLS is off (rollout bridge), and rejected once
// it is on. Returns ok=false to reject the connection.
func (srv *Server) wrapTLS(conn net.Conn) (net.Conn, bool) {
	if srv.TLSConfig == nil {
		return conn, true // TLS not configured yet; accept plain as before
	}
	// The caller already armed the single pre-HELLO deadline; do not extend it.
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil {
		return conn, false
	}
	pc := &prefixConn{Conn: conn, prefix: b}
	if b[0] == 0x16 { // TLS handshake record
		return tls.Server(pc, srv.TLSConfig), true
	}
	if srv.RequireTLS {
		srv.logf("tunnel from %s rejected: plaintext not allowed", conn.RemoteAddr())
		return conn, false
	}
	return pc, true
}

// prefixConn re-serves bytes already peeked from the underlying conn before
// reading the rest, so the TLS/plain detector does not consume the handshake.
type prefixConn struct {
	net.Conn
	prefix []byte
}

func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// hostOnly strips the port from an addr so caps key on the source IP.
func hostOnly(addr net.Addr) string {
	if h, _, err := net.SplitHostPort(addr.String()); err == nil {
		return h
	}
	return addr.String()
}
