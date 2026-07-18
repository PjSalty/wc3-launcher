// Package bnetgateway runs a local Battle.net proxy that Warcraft III connects
// to when its gateway address is pointed at 127.0.0.1. It forwards the realm
// session to an upstream: the relay when hosting, or the realm directly for
// testing.
//
// This is the host-side half of native hosting. The realm server advertises a
// created game at <source IP of the host's realm connection>:<the port WC3
// itself declared>. Routing WC3's realm session through the relay is what makes
// the first half true: the connection reaches the realm from the server's own
// address, so the server becomes the advertised host. The second half is set
// before the game ever starts, by writing the relay's allocated port into WC3's
// own config as its game port.
//
// So this proxy does not modify the stream at all. It is a plain byte pump. No
// packet parsing, no rewriting, nothing to desync. That is the entire point:
// the address is pinned by where the connection comes FROM and what WC3 was
// configured to declare, not by editing what it says.
package bnetgateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// Proxy is a local realm listener that forwards WC3's session upstream.
type Proxy struct {
	// Listen is the local address WC3's gateway points at, e.g. "127.0.0.1:6112".
	// Ignored when Listener is set.
	Listen string
	// Listener, when set, is a pre-bound listener the caller owns (used as the
	// single-instance guard: the launcher binds the gateway port before starting
	// the game, so a second copy fails fast instead of launching and then
	// colliding here). ListenAndServe uses it instead of binding Listen.
	Listener net.Listener
	// Upstream is the realm/relay address to forward the session to. Used only
	// when Dial is nil.
	Upstream string
	// Dial, when set, supplies the upstream instead of dialing Upstream. Relay
	// mode uses it to return a tunnel stream so the host's realm session rides
	// the relay and the realm sees the server's address as the game host.
	Dial func(ctx context.Context) (io.ReadWriteCloser, error)
	// Logger receives connection lifecycle lines; nil discards them.
	Logger *log.Logger
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Logger != nil {
		p.Logger.Printf(format, args...)
	}
}

// ListenAndServe accepts WC3 connections until ctx is cancelled or Accept
// fails. Each connection is proxied to a fresh upstream dial.
func (p *Proxy) ListenAndServe(ctx context.Context) error {
	if p.Listener == nil && p.Listen == "" {
		return errors.New("bnetgateway: Listen or Listener is required")
	}
	if p.Upstream == "" && p.Dial == nil {
		return errors.New("bnetgateway: one of Upstream/Dial is required")
	}
	ln := p.Listener
	if ln == nil {
		var lc net.ListenConfig
		var err error
		ln, err = lc.Listen(ctx, "tcp", p.Listen)
		if err != nil {
			return fmt.Errorf("bnetgateway: listen %s: %w", p.Listen, err)
		}
	}
	defer ln.Close()

	// Unblock Accept on cancellation.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	p.logf("listening on %s, upstream %s", p.Listen, p.Upstream)
	for {
		client, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("bnetgateway: accept: %w", err)
		}
		go p.handle(ctx, client)
	}
}

// handle proxies one WC3 connection to a fresh upstream dial, copying both
// directions verbatim.
//
// Verbatim is load-bearing, not laziness. WC3 opens the realm socket with a
// lone 0x01 protocol selector byte that is not part of the length-framed packet
// stream that follows, so anything that tries to parse the session as packets
// desyncs on the very first byte and the login never completes. Nothing here
// needs to read the stream anyway.
func (p *Proxy) handle(ctx context.Context, client net.Conn) {
	defer client.Close()

	up, err := p.dialUpstream(ctx)
	if err != nil {
		p.logf("upstream failed: %v", err)
		return
	}
	defer up.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if _, err := io.Copy(up, client); err != nil && !isClosed(err) {
			p.logf("client->upstream ended: %v", err)
		}
		halfClose(up) // let the upstream see EOF on this direction
	}()

	go func() {
		defer wg.Done()
		if _, err := io.Copy(client, up); err != nil && !isClosed(err) {
			p.logf("upstream->client ended: %v", err)
		}
		halfClose(client)
	}()

	wg.Wait()
}

// dialUpstream returns the upstream conn, from Dial if set else a TCP dial.
func (p *Proxy) dialUpstream(ctx context.Context) (io.ReadWriteCloser, error) {
	if p.Dial != nil {
		return p.Dial(ctx)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", p.Upstream)
}

// halfClose closes the write side when the conn supports it (orderly EOF to the
// peer), otherwise closes it fully.
func halfClose(c io.Closer) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

func isClosed(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF)
}
