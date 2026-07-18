package tunnel

import (
	"bytes"
	"testing"
)

// FuzzReadFrame locks in that the wire parser never panics on arbitrary input.
// The relay reads frames straight off an internet-facing socket, so malformed or
// hostile bytes must always produce an error, never a crash, and a successfully
// parsed frame must stay within the payload bound.
func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{Magic, TypeData, 0x08, 0x00, 0x01, 0x00, 0xAA, 0xBB})
	f.Add([]byte{Magic, TypeHello, 0x06, 0x00, 0x00, 0x00})
	f.Add([]byte{Magic, 0x00, 0x03, 0x00, 0x00, 0x00}) // length < header
	f.Add([]byte{0x00})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := ReadFrame(bytes.NewReader(data))
		if err == nil && len(fr.Payload) > MaxPayload {
			t.Fatalf("parsed payload %d exceeds MaxPayload %d", len(fr.Payload), MaxPayload)
		}
	})
}
