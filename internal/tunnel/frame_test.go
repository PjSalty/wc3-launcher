package tunnel

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	cases := []Frame{
		{Type: TypeHello, Stream: StreamControl, Payload: []byte("test-token")},
		{Type: TypeData, Stream: StreamFirstClient, Payload: []byte{0xFF, 0x1E, 0x04, 0x00}},
		{Type: TypeOpen, Stream: StreamFirstJoiner, Payload: nil},
		{Type: TypePing, Stream: StreamControl},
		{Type: TypeData, Stream: 7, Payload: bytes.Repeat([]byte{0xAB}, MaxPayload)},
	}
	var buf bytes.Buffer
	for _, f := range cases {
		if err := WriteFrame(&buf, f); err != nil {
			t.Fatalf("write %v: %v", f.Type, err)
		}
	}
	for i, want := range cases {
		got, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if got.Type != want.Type || got.Stream != want.Stream || !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("frame %d = {t:%d s:%d len:%d}, want {t:%d s:%d len:%d}",
				i, got.Type, got.Stream, len(got.Payload), want.Type, want.Stream, len(want.Payload))
		}
	}
	if _, err := ReadFrame(&buf); !errors.Is(err, io.EOF) {
		t.Errorf("after last frame: err = %v, want io.EOF", err)
	}
}

func TestWriteFrameRejectsOversizePayload(t *testing.T) {
	if err := WriteFrame(io.Discard, Frame{Type: TypeData, Payload: make([]byte, MaxPayload+1)}); err == nil {
		t.Fatal("expected error on oversize payload, got nil")
	}
}

func TestReadFrameBadMagic(t *testing.T) {
	if _, err := ReadFrame(bytes.NewReader([]byte{0x00, 1, 6, 0, 0, 0})); err == nil {
		t.Fatal("expected error on bad magic, got nil")
	}
}

func TestReadFrameTruncatedPayload(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteFrame(&buf, Frame{Type: TypeData, Stream: 2, Payload: []byte{1, 2, 3, 4}})
	b := buf.Bytes()
	if _, err := ReadFrame(bytes.NewReader(b[:len(b)-2])); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated frame: err = %v, want io.ErrUnexpectedEOF", err)
	}
}
