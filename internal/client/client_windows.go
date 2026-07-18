//go:build windows

package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Configure writes the Battle.net gateway list to the real HKCU registry. No
// admin rights are needed. dir is unused on Windows (the registry is global to
// the user).
func Configure(dir, host, name, timezone string) error {
	const keyPath = `Software\Blizzard Entertainment\Warcraft III`
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening HKCU\\%s: %w", keyPath, err)
	}
	defer key.Close()

	if err := key.SetStringsValue(gatewayValueName, gatewayStrings(host, timezone, name)); err != nil {
		return fmt.Errorf("writing %q value: %w", gatewayValueName, err)
	}
	return nil
}

// ExistingGateway returns the realm host and display name a prior launcher wrote
// into the WC3 registry, so an upgrading player is migrated without re-entering
// them. Best-effort: ok=false on any miss, so the caller falls back to setup.
// dir is unused on Windows (the registry is per-user global).
func ExistingGateway(dir string) (host, name string, ok bool) {
	const keyPath = `Software\Blizzard Entertainment\Warcraft III`
	key, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE)
	if err != nil {
		return "", "", false
	}
	defer key.Close()
	vals, _, err := key.GetStringsValue(gatewayValueName)
	if err != nil {
		return "", "", false
	}
	return parseGateway(vals)
}

// SetGamePort writes WC3's host game port (netgameport) so its game listener
// moves off 6112, freeing that port for the launcher's local BnetGateway in
// relay-host mode. dir is unused on Windows.
//
// The value type (REG_DWORD here) is the one thing to confirm on a real client:
// if a build reads netgameport as REG_SZ instead, WC3 ignores this and keeps
// 6112, clashing with the gateway. Verify during the first host test.
func SetGamePort(dir string, port int) error {
	const keyPath = `Software\Blizzard Entertainment\Warcraft III\Gameplay`
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("opening HKCU\\%s: %w", keyPath, err)
	}
	defer key.Close()
	if err := key.SetDWordValue("netgameport", uint32(port)); err != nil {
		return fmt.Errorf("writing netgameport: %w", err)
	}
	return nil
}

// Launch starts the game through the loader located in gameRoot (the Warcraft
// III install directory). dir is unused on Windows. classic launches Reign of
// Chaos instead of the default Frozen Throne (the loader's -classic switch).
//
// Returns the started command so a caller can Wait on it (relay-host mode exits
// when the game closes).
func Launch(dir, gameRoot, loaderExe string, classic bool) (*exec.Cmd, error) {
	loader := filepath.Join(gameRoot, loaderExe)
	if _, err := os.Stat(loader); err != nil {
		return nil, fmt.Errorf("loader %q not found in %s: %w", loaderExe, gameRoot, err)
	}
	var args []string
	if classic {
		args = append(args, "-classic")
	}
	cmd := exec.Command(loader, args...)
	cmd.Dir = gameRoot
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %q: %w", loaderExe, err)
	}
	return cmd, nil
}

// gameRunning reports whether a Warcraft III game process is alive, by walking
// the process list for one of gameProcessNames. The W3L loader (w3l.exe) exits
// right after it spawns the game, so this deliberately does not match it.
func gameRunning() bool {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return false
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		for _, g := range gameProcessNames {
			if name == g {
				return true
			}
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			return false
		}
	}
}
