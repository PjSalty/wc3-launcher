//go:build linux

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureShortcutWritesDesktopEntry checks the .desktop is written to the app
// menu with a space-safe Exec and the expected metadata. HOME points at a dir
// with no Desktop folder, so only the application-menu entry is exercised.
func TestEnsureShortcutWritesDesktopEntry(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("HOME", tmp)

	launcher := "/opt/WC3 App/wc3-launcher" // path with a space
	if err := EnsureShortcut(launcher, "/game"); err != nil {
		t.Fatalf("EnsureShortcut: %v", err)
	}

	p := filepath.Join(tmp, "data", "applications", "wc3-launcher.desktop")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("expected .desktop at %s: %v", p, err)
	}
	s := string(b)
	if !strings.Contains(s, `Exec="`+launcher+`"`) {
		t.Errorf("Exec not quoted for a path with spaces:\n%s", s)
	}
	if !strings.Contains(s, "Name="+shortcutName) {
		t.Errorf("missing Name=%q:\n%s", shortcutName, s)
	}
	if !strings.Contains(s, "Type=Application") {
		t.Errorf("missing Type=Application:\n%s", s)
	}
}
