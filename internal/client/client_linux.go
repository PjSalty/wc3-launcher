//go:build linux

package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// winePrefix keeps a dedicated Wine prefix inside the game folder so the
// launcher never touches the user's default ~/.wine.
func winePrefix(dir string) string { return filepath.Join(dir, "wineprefix") }

func wineEnv(dir string) []string {
	return append(os.Environ(),
		"WINEPREFIX="+winePrefix(dir),
		"WINEDEBUG=-all",
		// Route WC3's Direct3D 8 through the bundled d3d8to9 -> DXVK (d3d9) ->
		// Vulkan. Wine's default D3D->OpenGL path fails to choose a pixel format
		// on NVIDIA/Wayland and crashes the game on startup; DXVK is reliable.
		"WINEDLLOVERRIDES=d3d8=n,d3d9=n",
		// WINEDEBUG=-all silences Wine's own logs, but DXVK logs separately. Point
		// it at the game folder so a startup crash (e.g. no 32-bit Vulkan driver)
		// leaves a d3d9.log to diagnose instead of a silent flicker-and-exit.
		"DXVK_LOG_PATH="+dir,
	)
}

// wineLog routes Wine's verbose stderr (and any LD_PRELOAD/EGL noise the user's
// environment injects) to a log file so the launcher console stays readable.
// Returns nil on failure (caller keeps its existing stdio).
func wineLog(dir string) *os.File {
	f, err := os.OpenFile(filepath.Join(dir, "wine.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return f
}

func requireWine() (string, error) {
	path, err := exec.LookPath("wine")
	if err != nil {
		return "", fmt.Errorf("Wine is required to run Warcraft III on Linux; install it (for example `sudo apt install wine`, or via Lutris) and run the launcher again")
	}
	return path, nil
}

// ExistingGateway migrates a player off an older baked-in launcher by reading
// the realm it wrote into the game's Wine-prefix registry, so an upgrader is not
// asked to set it up again. Best-effort: ok=false on any miss (no Wine, no
// prefix, no value), so the caller falls back to the first-run prompt.
func ExistingGateway(dir string) (host, name string, ok bool) {
	wine, err := exec.LookPath("wine")
	if err != nil {
		return "", "", false
	}
	cmd := exec.Command(wine, "reg", "query", gatewayRegKey, "/v", gatewayValueName)
	cmd.Env = wineEnv(dir)
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	return parseWineGateway(string(out))
}

// parseWineGateway pulls the gateway host + name out of `wine reg query` output,
// whose value line reads: "<name>\tREG_MULTI_SZ\t1001\0<idx>\0host\0zone\0name\0
// ...", with each REG_MULTI_SZ entry rendered with a literal two-character `\0`
// separator. Splitting on that literal yields the positional list parseGateway
// expects.
func parseWineGateway(out string) (host, name string, ok bool) {
	const marker = "REG_MULTI_SZ"
	i := strings.Index(out, marker)
	if i < 0 {
		return "", "", false
	}
	data := strings.TrimSpace(out[i+len(marker):])
	return parseGateway(strings.Split(data, `\0`))
}

// Configure writes the Battle.net gateway value into the game's Wine prefix and
// initialises the prefix. It uses `wine reg add` deliberately, NOT a hex .reg
// import: reg add stores the REG_MULTI_SZ as single-byte ANSI, which is exactly
// what WC3's Game.dll reads back via RegQueryValueExA. A hand-built UTF-16 value
// is read as a one-character "w" and the gateway silently never appears.
func Configure(dir, host, name, timezone string) error {
	wine, err := requireWine()
	if err != nil {
		return err
	}
	// reg.exe interprets a literal "\0" in /d as the REG_MULTI_SZ separator.
	data := strings.Join(gatewayStrings(host, timezone, name), `\0`)
	cmd := exec.Command(wine, "reg", "add", gatewayRegKey,
		"/v", gatewayValueName, "/t", "REG_MULTI_SZ", "/d", data, "/f")
	cmd.Env = wineEnv(dir)
	if lf := wineLog(dir); lf != nil {
		defer lf.Close()
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("writing gateway into the Wine registry: %w", err)
	}
	return nil
}

// SetGamePort writes WC3's host game port (netgameport) into the Wine prefix
// registry so its game listener moves off 6112, freeing that port for the
// launcher's local BnetGateway in relay-host mode. See the Windows note on the
// REG_DWORD vs REG_SZ caveat.
func SetGamePort(dir string, port int) error {
	wine, err := requireWine()
	if err != nil {
		return err
	}
	cmd := exec.Command(wine, "reg", "add",
		`HKEY_CURRENT_USER\Software\Blizzard Entertainment\Warcraft III\Gameplay`,
		"/v", "netgameport", "/t", "REG_DWORD", "/d", strconv.Itoa(port), "/f")
	cmd.Env = wineEnv(dir)
	if lf := wineLog(dir); lf != nil {
		defer lf.Close()
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("writing netgameport into the Wine registry: %w", err)
	}
	return nil
}

// primaryResolution returns the resolution of the PRIMARY display (e.g.
// "1920x1080") for the Wine virtual desktop. On a multi-monitor setup it must be
// the main screen, not whichever output xrandr happens to list first (which is
// often a side/portrait monitor), so it reads the geometry of the output marked
// `primary` - the "WxH" of its "WxH+X+Y", which already reflects any rotation.
// Falls back to the first current mode, then 1920x1080 (headless, no xrandr).
func primaryResolution() string {
	// Primary output's displayed geometry: `... connected primary 3840x2160+2160+854 ...`
	if out, err := exec.Command("sh", "-c",
		`xrandr --current 2>/dev/null | awk '/ connected primary / {for (i=1;i<=NF;i++) if ($i ~ /^[0-9]+x[0-9]+\+/) {split($i,a,"+"); print a[1]; exit}}'`).Output(); err == nil {
		if res := strings.TrimSpace(string(out)); res != "" {
			return res
		}
	}
	// No primary flagged: fall back to the first current (*) mode.
	if out, err := exec.Command("sh", "-c", `xrandr --current 2>/dev/null | awk '/\*/{print $1; exit}'`).Output(); err == nil {
		if res := strings.TrimSpace(string(out)); res != "" {
			return res
		}
	}
	return "1920x1080"
}

// primaryResolutionWH parses primaryResolution into width and height, falling
// back to 1920x1080.
func primaryResolutionWH() (int, int) {
	w, h, ok := strings.Cut(primaryResolution(), "x")
	if !ok {
		return 1920, 1080
	}
	wi, err1 := strconv.Atoi(strings.TrimSpace(w))
	hi, err2 := strconv.Atoi(strings.TrimSpace(h))
	if err1 != nil || err2 != nil || wi <= 0 || hi <= 0 {
		return 1920, 1080
	}
	return wi, hi
}

// setResolution writes Warcraft III's internal render resolution into the Wine
// prefix registry so the game fills the virtual desktop (which we size to the
// display) instead of rendering at a small default and letterboxing.
func setResolution(dir, wine string, width, height int) error {
	for _, kv := range []struct {
		name string
		val  int
	}{{"reswidth", width}, {"resheight", height}} {
		cmd := exec.Command(wine, "reg", "add",
			`HKEY_CURRENT_USER\Software\Blizzard Entertainment\Warcraft III\Video`,
			"/v", kv.name, "/t", "REG_DWORD", "/d", strconv.Itoa(kv.val), "/f")
		cmd.Env = wineEnv(dir)
		if lf := wineLog(dir); lf != nil {
			cmd.Stdout, cmd.Stderr = lf, lf
			err := cmd.Run()
			lf.Close()
			if err != nil {
				return err
			}
			continue
		}
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	return nil
}

// Launch starts the game through the loader under Wine. gameRoot is the
// Warcraft III install directory inside the Wine prefix; dir provides the prefix.
// classic launches Reign of Chaos instead of the default Frozen Throne.
//
// The game runs inside a Wine virtual desktop by default. That fixes two
// Wayland/Wine problems at once: an exclusive-fullscreen game can't be
// alt-tabbed back into, and its in-game resolution change doesn't take effect.
// A virtual desktop is a normal, tab-able window sized to the display, and WC3
// renders its chosen resolution inside it. Set WC3_FULLSCREEN=1 to opt back
// into raw fullscreen.
//
// Returns the started command so a caller can Wait on it (relay-host mode exits
// when the game closes). The `wine explorer /desktop` process is the virtual
// desktop and stays alive until that window is closed, so its exit tracks the
// game's exit.
func Launch(dir, gameRoot, loaderExe string, classic bool) (*exec.Cmd, error) {
	wine, err := requireWine()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(gameRoot, loaderExe)); err != nil {
		return nil, fmt.Errorf("loader %q not found in %s: %w", loaderExe, gameRoot, err)
	}
	// Match the game's render resolution to the display (best-effort).
	if w, h := primaryResolutionWH(); w > 0 && h > 0 {
		_ = setResolution(dir, wine, w, h)
	}
	var args []string
	if os.Getenv("WC3_FULLSCREEN") == "" {
		args = append(args, "explorer", "/desktop=WC3,"+primaryResolution())
	}
	args = append(args, loaderExe)
	if classic {
		args = append(args, "-classic")
	}
	cmd := exec.Command(wine, args...)
	cmd.Dir = gameRoot
	cmd.Env = wineEnv(dir)
	// Not closed here: the game runs on after this returns and keeps writing.
	// The launcher exits shortly after, which releases the descriptor.
	if lf := wineLog(dir); lf != nil {
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting Warcraft III via Wine: %w", err)
	}
	return cmd, nil
}

// gameRunning reports whether a Warcraft III game process is alive, by scanning
// /proc for one of gameProcessNames in a process command line. The W3L loader
// (w3l.exe) and the `wine explorer` desktop do not match their own arguments to
// the game exe, so this tracks the game itself, not the launcher scaffolding.
func gameRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue // only numeric pid directories
		}
		data, err := os.ReadFile("/proc/" + name + "/cmdline")
		if err != nil || len(data) == 0 {
			continue
		}
		cmd := strings.ToLower(strings.ReplaceAll(string(data), "\x00", " "))
		for _, g := range gameProcessNames {
			if strings.Contains(cmd, g) {
				return true
			}
		}
	}
	return false
}
