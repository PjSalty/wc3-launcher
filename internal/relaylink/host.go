package relaylink

import (
	"context"
	"io"
	"log"
	"net"

	"wc3-launcher/internal/bnetgateway"
)

// ServeHost runs the resident local gateway for an already-dialed relay Link on
// gate, a listener the caller pre-bound (127.0.0.1:6112). Binding it up front is
// the launcher's single-instance guard: a second launcher fails the bind and
// exits before touching the game, instead of starting WC3 and colliding here.
// Every WC3 connection to gate opens its own stream to the relay, which dials
// pvpgn per stream (a native login opens several: the realm connection plus the
// BNFTP version-check download). Blocks until ctx is cancelled or the tunnel
// drops; a dropped tunnel returns ErrTunnelLost.
func ServeHost(ctx context.Context, link *Link, gate net.Listener, logger *log.Logger) error {
	defer link.GameOver()
	// With the tunnel gone the gateway has nothing behind it: it would go on
	// accepting WC3's reconnects and dropping each one while the launcher still
	// says it is ready to host. Stop serving and say what happened instead.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-link.Done():
			cancel()
		case <-sctx.Done():
		}
	}()
	dial := func(context.Context) (io.ReadWriteCloser, error) {
		return link.OpenClientStream()
	}
	proxy := &bnetgateway.Proxy{Listener: gate, Dial: dial, Logger: logger}
	err := proxy.ListenAndServe(sctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-link.Done():
		return ErrTunnelLost
	default:
		return err
	}
}

// RunHost dials the relay and serves the host gateway in one call. Callers that
// want to fall back to a direct connection when the relay is unreachable should
// call Dial + ServeHost themselves and branch on Dial's error.
func RunHost(ctx context.Context, relayAddr, token, gateAddr string, gamePort int, logger *log.Logger) error {
	gate, err := net.Listen("tcp", gateAddr)
	if err != nil {
		return err
	}
	link, _, err := Dial(ctx, relayAddr, token, gamePort, logger, nil)
	if err != nil {
		gate.Close()
		return err
	}
	return ServeHost(ctx, link, gate, logger)
}
