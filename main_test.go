package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestChooseClassicCancels pins the Ctrl-C behavior at the game-selection prompt.
// The bug this guards: in relay-host mode run() installs a signal.NotifyContext
// for os.Interrupt, which disables Go's default terminate-on-SIGINT. The prompt
// then blocked on stdin forever, so Ctrl-C did nothing and the only way out was
// closing the window, which left the gateway port held and produced a stale
// "already running" on the next launch.
func TestChooseClassicCancels(t *testing.T) {
	// stdin is a pipe we never write to, so the read blocks and only the context
	// can end the prompt: exactly the situation the fix addresses.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled context stands in for the Ctrl-C signal

	done := make(chan error, 1)
	go func() {
		_, err := chooseClassic(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errCancelled) {
			t.Fatalf("chooseClassic on cancel = %v, want errCancelled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("chooseClassic ignored cancellation and kept blocking on stdin")
	}
}

// TestChooseClassicReadsChoice proves normal input still works, and that EOF
// (piped/closed stdin) takes the documented default instead of being treated as
// a cancellation.
func TestChooseClassicReadsChoice(t *testing.T) {
	run := func(t *testing.T, input string) (bool, error) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatalf("pipe: %v", err)
		}
		orig := os.Stdin
		os.Stdin = r
		t.Cleanup(func() { os.Stdin = orig; r.Close() })
		go func() { _, _ = w.WriteString(input); w.Close() }()
		return chooseClassic(context.Background())
	}

	if got, err := run(t, "2\n"); err != nil || !got {
		t.Fatalf(`input "2" = (%v,%v), want (true,nil) for Reign of Chaos`, got, err)
	}
	if got, err := run(t, "1\n"); err != nil || got {
		t.Fatalf(`input "1" = (%v,%v), want (false,nil)`, got, err)
	}
	if got, err := run(t, "\n"); err != nil || got {
		t.Fatalf(`empty input = (%v,%v), want (false,nil) default`, got, err)
	}
	if got, err := run(t, ""); err != nil || got {
		t.Fatalf(`EOF = (%v,%v), want (false,nil) default, not a cancellation`, got, err)
	}
}
