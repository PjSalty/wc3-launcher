package relay

import (
	"net"
	"testing"
	"time"
)

// TestRelayConnDeliverNeverBlocks is the head-of-line-block guard for the relay
// demux: deliver runs on the single tunnel read loop, so a peer that stops
// reading must never block it. net.Pipe is synchronous, so with nothing reading
// the far end the socket write stalls; once the bounded queue fills, the stream
// must drop ITSELF rather than freeze every other stream on the session.
func TestRelayConnDeliverNeverBlocks(t *testing.T) {
	stalled, _ := net.Pipe() // far end is never read, so writes block forever
	rc := newRelayConn(stalled)

	done := make(chan struct{})
	go func() {
		for i := 0; i < connOutCap+100; i++ {
			rc.deliver([]byte{1})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliver blocked on a stalled peer: it would head-of-line-block the demux and freeze every stream")
	}

	select {
	case <-rc.closed:
	default:
		t.Fatal("relayConn was not shut down after overflowing its bounded queue")
	}
}
