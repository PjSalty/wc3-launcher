package relaylink

import (
	"testing"
	"time"
)

// TestStreamDeliverNeverBlocks is the head-of-line-block guard: deliver runs on
// the single shared demux goroutine, so it must never block on a slow/stalled
// reader. A stream whose reader never drains it must, after its bounded queue
// fills, drop ITSELF (shut down) instead of stalling every other stream.
func TestStreamDeliverNeverBlocks(t *testing.T) {
	l := &Link{streams: make(map[uint16]*stream)}
	s := newStreamState(2, l) // nothing ever Reads this stream

	done := make(chan struct{})
	go func() {
		for i := 0; i < inboxCap+100; i++ {
			s.deliver([]byte{1})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliver blocked with no reader; it would head-of-line-block the demux and freeze every stream")
	}

	select {
	case <-s.closed:
	default:
		t.Fatal("stream was not shut down after overflowing its bounded queue")
	}
}
