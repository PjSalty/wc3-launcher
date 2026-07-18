//go:build windows

package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// InstallPath returns the Warcraft III install directory if the game is already
// installed, from Blizzard's registry key. dir is unused on Windows.
func InstallPath(dir string) (string, bool) {
	for _, base := range []string{
		`SOFTWARE\Blizzard Entertainment\Warcraft III`,
		`SOFTWARE\WOW6432Node\Blizzard Entertainment\Warcraft III`,
	} {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, base, registry.QUERY_VALUE|registry.WOW64_32KEY)
		if err != nil {
			continue
		}
		p, _, err := k.GetStringValue("InstallPath")
		k.Close()
		if err == nil && hasGame(p) {
			return p, true
		}
	}
	// Fallback: a copied or keyless install that never wrote the registry key.
	// Probe the common install folders so an existing game is still reused
	// instead of triggering a reinstall + CD-key prompt.
	for _, p := range commonInstallDirs() {
		if hasGame(p) {
			return p, true
		}
	}
	return "", false
}

// commonInstallDirs lists the standard Warcraft III install folders to probe
// when the registry key is absent.
func commonInstallDirs() []string {
	var dirs []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramW6432", "ProgramFiles"} {
		if base := os.Getenv(env); base != "" {
			dirs = append(dirs, filepath.Join(base, "Warcraft III"))
		}
	}
	return append(dirs,
		`C:\Program Files (x86)\Warcraft III`,
		`C:\Program Files\Warcraft III`,
	)
}

func hasGame(dir string) bool {
	if dir == "" {
		return false
	}
	for _, exe := range []string{"Warcraft III.exe", "war3.exe", "Frozen Throne.exe"} {
		if _, err := os.Stat(filepath.Join(dir, exe)); err == nil {
			return true
		}
	}
	return false
}

// Run executes Blizzard's installer and waits for it to finish (it is
// interactive: the user follows Blizzard's prompts, including the CD key). dir
// is unused on Windows.
func Run(dir, installerPath string) error {
	// The old installer raises a scary "Windows Help / open this .hlp" dialog on
	// modern Windows. Auto-close it for the whole install so non-technical
	// players never see it. Safe by construction: only help-specific windows
	// match, never the installer's own Setup/CD-key prompts (see dismiss.go).
	stop := make(chan struct{})
	go watchAndDismissHelp(stop)
	defer close(stop)

	cmd := exec.Command(installerPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running Blizzard installer: %w", err)
	}
	return nil
}
