package wineenv

import (
	"fmt"
	"os"
	"testing"
)

// TestRuntimeDirEnv covers the Bazzite session exactly: XDG_RUNTIME_DIR empty
// while /run/user/<uid> exists and is fine. Wine then aborts with
// "XDG_RUNTIME_DIR is invalid or not set", taking the Blizzard installer with it.
func TestRuntimeDirEnv(t *testing.T) {
	real := fmt.Sprintf("/run/user/%d", os.Getuid())
	_, haveReal := os.Stat(real)

	good := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", good)
	if got := RuntimeDirEnv(); got != nil {
		t.Fatalf("a valid XDG_RUNTIME_DIR must be left alone, got %v", got)
	}

	for _, bad := range []string{"", "/nonexistent/xdg"} {
		t.Setenv("XDG_RUNTIME_DIR", bad)
		got := RuntimeDirEnv()
		if haveReal == nil {
			if len(got) != 1 || got[0] != "XDG_RUNTIME_DIR="+real {
				t.Fatalf("XDG_RUNTIME_DIR=%q should be repaired to %s, got %v", bad, real, got)
			}
		} else if got != nil {
			t.Fatalf("no /run/user dir exists, so nothing should be set, got %v", got)
		}
	}
}

// TestHasDisplay pins the check that turns a bare "exit status 1" into an
// explanation. The Bazzite run had both variables empty (an SSH/TTY session).
func TestHasDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if HasDisplay() {
		t.Fatal("no display variables set, HasDisplay must be false")
	}
	t.Setenv("DISPLAY", ":0")
	if !HasDisplay() {
		t.Fatal("DISPLAY set, HasDisplay must be true")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if !HasDisplay() {
		t.Fatal("WAYLAND_DISPLAY set, HasDisplay must be true")
	}
}
