//go:build linux

package client

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestRuntimeDirEnv covers the Bazzite failure: a session with no usable
// XDG_RUNTIME_DIR made Wine abort with "XDG_RUNTIME_DIR is invalid or not set",
// which took the Blizzard installer down with it. A working session must never
// be overridden; a broken one must be repaired when /run/user/<uid> exists.
func TestRuntimeDirEnv(t *testing.T) {
	real := fmt.Sprintf("/run/user/%d", os.Getuid())
	_, haveReal := os.Stat(real)

	// A valid value is left completely alone.
	good := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", good)
	if got := runtimeDirEnv(); got != nil {
		t.Fatalf("a valid XDG_RUNTIME_DIR must not be overridden, got %v", got)
	}

	// Unset, and pointing at something that does not exist, are both repaired
	// (only assertable where the standard runtime dir actually exists).
	for _, bad := range []string{"", "/nonexistent/xdg/runtime"} {
		t.Setenv("XDG_RUNTIME_DIR", bad)
		got := runtimeDirEnv()
		if haveReal == nil {
			if len(got) != 1 || got[0] != "XDG_RUNTIME_DIR="+real {
				t.Fatalf("XDG_RUNTIME_DIR=%q should be repaired to %s, got %v", bad, real, got)
			}
		} else if got != nil {
			t.Fatalf("no /run/user dir to fall back to, so nothing should be set, got %v", got)
		}
	}
}

// TestWineEnvCarriesRuntimeDir proves the repair actually reaches Wine, not just
// the helper: wineEnv is what every Wine invocation uses.
func TestWineEnvCarriesRuntimeDir(t *testing.T) {
	if _, err := os.Stat(fmt.Sprintf("/run/user/%d", os.Getuid())); err != nil {
		t.Skip("no /run/user/<uid> on this box; nothing to fall back to")
	}
	t.Setenv("XDG_RUNTIME_DIR", "/nonexistent/xdg/runtime")
	var seen string
	for _, kv := range wineEnv(t.TempDir()) {
		if strings.HasPrefix(kv, "XDG_RUNTIME_DIR=") {
			seen = kv // last wins, which is what exec uses
		}
	}
	if seen != fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()) {
		t.Fatalf("wineEnv did not carry the repaired runtime dir, got %q", seen)
	}
}
