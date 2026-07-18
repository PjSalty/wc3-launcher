// Package tunnel is the wire protocol between a host player's launcher and the
// relay daemon. One outbound TLS connection (launcher -> relay, so zero inbound
// / zero router config) multiplexes three planes by a 6-byte outer header:
//
//	[0xF9 magic][type][uint16 LE total length incl header][uint16 LE streamID][payload]
//
// Streams: 0 = control; odd ids = launcher-initiated, one per WC3 -> pvpgn
// connection (a native login opens several: the realm connection plus BNFTP
// version-check transfers), the relay dialing pvpgn:6112 for each and binding
// the first stream's source to the public port; even ids >= 2 = relay-initiated,
// one per joiner landing on the game's public port. The relay never parses a
// DATA payload; it is opaque BNCS (odd streams) or W3GS (even streams).
//
// 0xF9 sits alongside aura's W3GS 0xF7 / GPS 0xF8, which share the
// [magic][type][uint16 LE len] convention.
package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Magic is the first byte of every tunnel frame.
const Magic = 0xF9

// HeaderLen is the fixed outer header: magic, type, uint16 length, uint16 stream.
const HeaderLen = 6

// MaxFrameLen is the ceiling of the uint16 length field; MaxPayload is what
// remains after the header. A larger socket read is chunked across DATA frames
// by the caller.
const (
	MaxFrameLen = 65535
	MaxPayload  = MaxFrameLen - HeaderLen
)

// Frame types.
const (
	TypeHello    = 1 // launcher -> relay: {proto, account, realm_token, game_name}
	TypeHelloAck = 2 // relay -> launcher: {allocated port P, public IP, heartbeat}
	TypeOpen     = 3 // relay -> launcher: a joiner landed on port P (assigns stream)
	TypeData     = 4 // both ways: opaque bytes for a stream
	TypeClose    = 5 // both ways: stream ended
	TypePing     = 6
	TypePong     = 7
	TypeGameOver = 8 // launcher -> relay: game ended, release the port
	TypeError    = 9 // relay -> launcher: {code} e.g. pool_full / bad_token
)

// Reserved stream IDs. Odd ids are launcher-initiated (each a WC3 -> pvpgn
// connection); even ids >= 2 are relay-initiated (each a joiner). This split
// lets both ends open streams without colliding.
const (
	StreamControl     = 0 // HELLO/ACK/PING/PONG/GAMEOVER/ERROR
	StreamFirstClient = 1 // first launcher stream = host realm; relay binds its source to port P
	StreamFirstJoiner = 2 // first relay stream = a joiner on the public port
)

// Frame is one decoded tunnel message.
type Frame struct {
	Type    byte
	Stream  uint16
	Payload []byte
}

// WriteFrame encodes f to w as a single framed message.
func WriteFrame(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxPayload {
		return fmt.Errorf("tunnel: payload %d exceeds max %d", len(f.Payload), MaxPayload)
	}
	total := HeaderLen + len(f.Payload)
	hdr := [HeaderLen]byte{Magic, f.Type}
	binary.LittleEndian.PutUint16(hdr[2:4], uint16(total))
	binary.LittleEndian.PutUint16(hdr[4:6], f.Stream)
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads exactly one frame from r, blocking until it is complete.
func ReadFrame(r io.Reader) (Frame, error) {
	var hdr [HeaderLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	if hdr[0] != Magic {
		return Frame{}, fmt.Errorf("tunnel: bad magic 0x%02x, stream desynced", hdr[0])
	}
	total := int(binary.LittleEndian.Uint16(hdr[2:4]))
	if total < HeaderLen {
		return Frame{}, fmt.Errorf("tunnel: bad length %d", total)
	}
	f := Frame{Type: hdr[1], Stream: binary.LittleEndian.Uint16(hdr[4:6])}
	if total > HeaderLen {
		f.Payload = make([]byte, total-HeaderLen)
		if _, err := io.ReadFull(r, f.Payload); err != nil {
			return Frame{}, err
		}
	}
	return f, nil
}
