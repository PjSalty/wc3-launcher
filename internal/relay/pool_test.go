package relay

import "testing"

func TestPoolAllocReleaseExhaust(t *testing.T) {
	p := NewPool(6200, 6202) // 3 ports
	if got := p.Available(); got != 3 {
		t.Fatalf("Available = %d, want 3", got)
	}
	var got []uint16
	for i := 0; i < 3; i++ {
		port, err := p.Alloc()
		if err != nil {
			t.Fatalf("Alloc %d: %v", i, err)
		}
		got = append(got, port)
	}
	if _, err := p.Alloc(); err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
	p.Release(got[1])
	if p.Available() != 1 {
		t.Fatalf("Available after release = %d, want 1", p.Available())
	}
	reused, err := p.Alloc()
	if err != nil {
		t.Fatalf("Alloc after release: %v", err)
	}
	if reused != got[1] {
		t.Errorf("reused port = %d, want the released %d", reused, got[1])
	}
}

func TestPoolReleaseUnknownIsNoop(t *testing.T) {
	p := NewPool(6200, 6200)
	p.Release(9999) // never allocated
	if p.Available() != 1 {
		t.Errorf("Available = %d, want 1 (unchanged)", p.Available())
	}
}
