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
// BNFTP version-check download). Blocks until ctx is cancelled or the tunnel drops.
func ServeHost(ctx context.Context, link *Link, gate net.Listener, logger *log.Logger) error {
	defer link.GameOver()
	dial := func(context.Context) (io.ReadWriteCloser, error) {
		return link.OpenClientStream()
	}
	proxy := &bnetgateway.Proxy{Listener: gate, Dial: dial, Logger: logger}
	return proxy.ListenAndServe(ctx)
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
