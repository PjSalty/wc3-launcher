//go:build linux

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// winePrefix mirrors the client package: a dedicated prefix inside the game
// folder so the launcher never touches the user's default ~/.wine.
func winePrefix(dir string) string { return filepath.Join(dir, "wineprefix") }

// wineLog opens the shared Wine output log. Wine (and any LD_PRELOAD/EGL noise
// the user's environment injects) is verbose on stderr; routing it to a file
// keeps the launcher console readable. Returns nil on failure (caller keeps
// whatever stdio it had).
func wineLog(dir string) *os.File {
	f, err := os.OpenFile(filepath.Join(dir, "wine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return f
}

// InstallPath returns the Warcraft III directory inside the game's Wine prefix
// if the game is already installed there.
func InstallPath(dir string) (string, bool) {
	prefix := winePrefix(dir)
	for _, rel := range []string{
		"drive_c/Program Files (x86)/Warcraft III",
		"drive_c/Program Files/Warcraft III",
	} {
		p := filepath.Join(prefix, rel)
		for _, exe := range []string{"Warcraft III.exe", "war3.exe", "Frozen Throne.exe"} {
			if _, err := os.Stat(filepath.Join(p, exe)); err == nil {
				return p, true
			}
		}
	}
	return "", false
}

// Run executes Blizzard's installer under Wine and waits for it to finish.
func Run(dir, installerPath string) error {
	wine, err := exec.LookPath("wine")
	if err != nil {
		return fmt.Errorf("Wine is required to install Warcraft III on Linux; install it (for example `sudo apt install wine`) and re-run")
	}
	cmd := exec.Command(wine, installerPath)
	cmd.Env = append(os.Environ(), "WINEPREFIX="+winePrefix(dir), "WINEDEBUG=-all")
	logPath := filepath.Join(dir, "wine.log")
	if lf := wineLog(dir); lf != nil {
		defer lf.Close()
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running Blizzard installer under Wine: %w%s", err, wineLogTail(logPath, 20))
	}
	return nil
}

// wineLogTail returns a formatted tail of the Wine log so a failure shows the
// real cause (missing 32-bit support, prefix init error, etc.) instead of a bare
// "exit status 1". Empty when the log is missing or empty.
func wineLogTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "\n\n--- last lines of " + path + " (Wine output) ---\n" + strings.Join(lines, "\n")
}
