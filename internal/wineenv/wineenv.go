// Package wineenv holds the environment repairs every Wine invocation needs.
//
// It exists because they were previously duplicated. internal/client built one
// environment for running the game and internal/installer built another for the
// Blizzard installer, so a fix applied to one silently missed the other: v1.3.9
// repaired XDG_RUNTIME_DIR for the game and the installer kept failing in
// exactly the same way, because it never used that code. One home for both.
package wineenv

import (
	"fmt"
	"os"
)

// RuntimeDirEnv returns an XDG_RUNTIME_DIR override when the session does not
// provide a usable one, or nil when it already does.
//
// Wine's Wayland and D-Bus paths require it and fail with "XDG_RUNTIME_DIR is
// invalid or not set in the environment" without it. It is empty more often
// than expected: a bare TTY, an SSH session, some immutable-distro shells.
// Observed on Bazzite, where XDG_RUNTIME_DIR was empty while /run/user/1000
// existed and was correct. Only ever set when the standard directory really
// exists, so a working session is never overridden.
func RuntimeDirEnv() []string {
	if p := os.Getenv("XDG_RUNTIME_DIR"); p != "" {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return nil
		}
	}
	fallback := fmt.Sprintf("/run/user/%d", os.Getuid())
	if fi, err := os.Stat(fallback); err == nil && fi.IsDir() {
		return []string{"XDG_RUNTIME_DIR=" + fallback}
	}
	return nil
}

// HasDisplay reports whether this session can show a window at all.
//
// Both the Blizzard installer and the game are graphical. Run from a session
// with no display (SSH, a plain TTY) they fail deep inside Wine with a bare
// "exit status 1", which tells the player nothing. Checking first turns that
// into an explanation.
func HasDisplay() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

// NoDisplayHelp is the message shown when there is no display. It names the
// cause and the fix, because "exit status 1" sent a player back to whoever gave
// them the launcher.
const NoDisplayHelp = `This session has no graphical display, so Warcraft III cannot be
installed or played from here.

  WAYLAND_DISPLAY and DISPLAY are both empty, which usually means this is
  an SSH connection or a text-only console.

  Run the launcher from the desktop session on that machine instead: log in
  normally, open a terminal there, and run it again.`
