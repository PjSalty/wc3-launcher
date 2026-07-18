//go:build linux

package desktop

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureShortcut writes a .desktop entry into the application menu and (if a
// Desktop folder exists) onto the desktop, pointing at launcherPath. gameRoot is
// unused on Linux; the game runs through the launcher's own Wine prefix, so a
// generic game icon is used. Runs in a terminal so the player sees progress.
func EnsureShortcut(launcherPath, gameRoot string) error {
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=" + shortcutName + "\n" +
		"Comment=Play Warcraft III on a PvPGN server\n" +
		"Exec=" + quoteExec(launcherPath) + "\n" +
		"Icon=applications-games\n" +
		"Terminal=true\n" +
		"Categories=Game;\n"

	var wrote bool
	var lastErr error

	// Application menu (always attempt).
	appsDir := filepath.Join(dataDir(), "applications")
	if err := os.MkdirAll(appsDir, 0o755); err == nil {
		p := filepath.Join(appsDir, "wc3-launcher.desktop")
		if err := os.WriteFile(p, []byte(content), 0o755); err == nil {
			wrote = true
		} else {
			lastErr = err
		}
	} else {
		lastErr = err
	}

	// Desktop icon (only if a Desktop folder exists).
	if home, err := os.UserHomeDir(); err == nil {
		desk := filepath.Join(home, "Desktop")
		if fi, err := os.Stat(desk); err == nil && fi.IsDir() {
			p := filepath.Join(desk, "wc3-launcher.desktop")
			if err := os.WriteFile(p, []byte(content), 0o755); err == nil {
				wrote = true
			} else {
				lastErr = err
			}
		}
	}

	if !wrote {
		if lastErr == nil {
			lastErr = fmt.Errorf("no writable application or desktop location")
		}
		return lastErr
	}
	return nil
}

// dataDir returns $XDG_DATA_HOME or ~/.local/share.
func dataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return filepath.Join(home, ".local", "share")
}

// quoteExec wraps a path in double quotes if it contains spaces, per the
// .desktop Exec spec.
func quoteExec(path string) string {
	for _, r := range path {
		if r == ' ' || r == '\t' {
			return `"` + path + `"`
		}
	}
	return path
}

// RepointGameShortcuts is a no-op on Linux: the game lives inside the launcher's
// own Wine prefix, so there is no stock Warcraft III shortcut to redirect. The
// .desktop entry above is the only way in.
func RepointGameShortcuts(launcherPath, gameRoot string) (int, error) { return 0, nil }
