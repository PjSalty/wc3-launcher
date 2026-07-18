package relaylink

import (
	"io"
	"sync"

	"wc3-launcher/internal/tunnel"
)

// inboxCap bounds a single stream's pending-delivery queue. A consumer that
// falls this far behind (e.g. a stalled joiner on bad wifi) has its OWN stream
// dropped rather than blocking the shared demux loop and freezing every other
// stream, including the host's realm connection.
const inboxCap = 512

// stream is one multiplexed stream over the tunnel, presented as an
// io.ReadWriteCloser. Writes become DATA frames; incoming DATA payloads are
// queued and a per-stream pump drains them into a pipe that Read consumes, so a
// slow reader never head-of-line-blocks the demux.
type stream struct {
	id   uint16
	link *Link
	pr   *io.PipeReader
	pw   *io.PipeWriter

	inbox     chan []byte
	closed    chan struct{}
	closeOnce sync.Once // guards close(closed)+pw.Close
	sentClose sync.Once // guards sending the CLOSE frame exactly once
}

// newStreamState builds a stream and starts its pump. Payloads handed to
// deliver are owned by the caller's frame (ReadFrame allocates per frame), so
// they are safe to queue without copying.
func newStreamState(id uint16, link *Link) *stream {
	pr, pw := io.Pipe()
	s := &stream{
		id:     id,
		link:   link,
		pr:     pr,
		pw:     pw,
		inbox:  make(chan []byte, inboxCap),
		closed: make(chan struct{}),
	}
	go s.pump()
	return s
}

// pump drains queued payloads into the read pipe until the stream closes.
func (s *stream) pump() {
	for {
		select {
		case b := <-s.inbox:
			if _, err := s.pw.Write(b); err != nil {
				return
			}
		case <-s.closed:
			return
		}
	}
}

func (s *stream) Read(b []byte) (int, error) { return s.pr.Read(b) }

func (s *stream) Write(b []byte) (int, error) {
	if err := s.link.sendData(s.id, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// shutdown ends the stream (Read returns EOF, pump stops). Idempotent, sends no
// frame.
func (s *stream) shutdown() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.pw.Close()
	})
}

// Close tells the relay to release its end (CLOSE frame, once) and shuts the
// stream down.
func (s *stream) Close() error {
	s.sentClose.Do(func() {
		_ = s.link.send(tunnel.Frame{Type: tunnel.TypeClose, Stream: s.id})
	})
	s.shutdown()
	return nil
}

// deliver queues an incoming payload for the reader WITHOUT blocking the demux.
// If the reader has fallen inboxCap chunks behind, this stream is shut down (the
// slow consumer is dropped) rather than stalling every other stream; the CLOSE
// frame to the relay is sent later from the bridge's teardown, off the demux.
func (s *stream) deliver(b []byte) {
	select {
	case s.inbox <- b:
	case <-s.closed:
	default:
		s.shutdown()
	}
}

// closeIncoming ends the stream without sending CLOSE (used when the relay
// already told us to close).
func (s *stream) closeIncoming() { s.shutdown() }
