//go:build linux

package client

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"wc3-launcher/internal/wineenv"
)

// HostDiagnostics reports the host facts that decide whether Warcraft III can
// run under Wine, in the order they fail in practice: no display beats a missing
// Vulkan loader beats a missing codec.
//
// Read-only by contract. It installs nothing and prompts for nothing, so it is
// safe to run when the launcher itself will not start.
func HostDiagnostics(gameDir string) []Diag {
	d := detectDistro()
	a := detectWineArch()
	return []Diag{
		distroDiag(d),
		displayDiag(),
		runtimeDirDiag(),
		wineDiag(a),
		winePrefixDiag(gameDir),
		vulkanDiag(a),
		gstreamerDiag(d, a),
	}
}

func distroDiag(d distro) Diag {
	detail := d.pretty
	if fam := d.family(); fam != "unknown" {
		detail += fmt.Sprintf(" (%s family)", fam)
	} else {
		detail += " (unrecognised family: automatic package installs are disabled)"
	}
	if d.immutable {
		return Diag{"distro", DiagWarn, detail + "; immutable base image, so packages cannot be layered inline and installs are shown rather than run"}
	}
	return Diag{"distro", DiagInfo, detail}
}

// displayDiag is deliberately first among the failures. A session with no
// display fails deep inside Wine with a bare "exit status 1", which is what sent
// a player back to whoever gave them the launcher instead of to the fix.
func displayDiag() Diag {
	if v := os.Getenv("WAYLAND_DISPLAY"); v != "" {
		return Diag{"display", DiagOK, "WAYLAND_DISPLAY=" + v}
	}
	if v := os.Getenv("DISPLAY"); v != "" {
		return Diag{"display", DiagOK, "DISPLAY=" + v}
	}
	return Diag{"display", DiagFail, "WAYLAND_DISPLAY and DISPLAY are both empty, so this session cannot show a window. Usually an SSH connection or a text console. Log in at the machine's own desktop and run the launcher from a terminal there."}
}

func runtimeDirDiag() Diag {
	cur := os.Getenv("XDG_RUNTIME_DIR")
	repair := wineenv.RuntimeDirEnv()
	if repair == nil {
		if cur == "" {
			return Diag{"xdg-runtime-dir", DiagFail, fmt.Sprintf("unset, and /run/user/%d does not exist; Wine's Wayland and D-Bus paths fail without it", os.Getuid())}
		}
		return Diag{"xdg-runtime-dir", DiagOK, cur}
	}
	was := cur
	if was == "" {
		was = "empty"
	}
	return Diag{"xdg-runtime-dir", DiagWarn, fmt.Sprintf("session value unusable (%s); the launcher overrides it with %s", was, strings.TrimPrefix(repair[0], "XDG_RUNTIME_DIR="))}
}

// wineDiag reports which Wine build is installed, because that single fact
// decides whether the 32-bit or the 64-bit host libraries are the ones that
// matter, and getting it wrong is what made earlier versions demand 32-bit
// packages that were neither needed nor installable.
func wineDiag(a wineArch) Diag {
	p, err := exec.LookPath("wine")
	if err != nil {
		return Diag{"wine", DiagFail, "not found on PATH; the launcher offers to install it on first run"}
	}
	detail := p
	if v := wineVersion(); v != "" {
		detail += " - " + v
	}
	switch a {
	case wineArchMultilib:
		return Diag{"wine", DiagOK, detail + " - classic multilib build, so the 32-bit host libraries are the ones that matter"}
	case wineArchNewWoW64:
		return Diag{"wine", DiagOK, detail + " - new WoW64 build, so the 64-bit host libraries are the ones that matter"}
	}
	return Diag{"wine", DiagWarn, detail + " - could not tell multilib from new WoW64: no i386-unix or x86_64-unix loader directory under " +
		strings.Join(wineLibRoots(), ", ") + ". Both bitnesses are checked, so a real gap still prompts."}
}

// wineVersion runs `wine --version`, which prints and exits without starting a
// wineserver or touching a prefix.
func wineVersion() string {
	out, err := exec.Command("wine", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// winePrefixDiag reports the launcher's dedicated prefix. It is a separate
// prefix on purpose, so the player's own ~/.wine is never modified; saying so in
// the report heads off the "will this break my other Wine apps" question.
func winePrefixDiag(gameDir string) Diag {
	if gameDir == "" {
		return Diag{"wine-prefix", DiagInfo, "no game folder resolved yet"}
	}
	p := winePrefix(gameDir)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return Diag{"wine-prefix", DiagOK, p + " (dedicated; your own ~/.wine is untouched)"}
	}
	return Diag{"wine-prefix", DiagInfo, p + " - not created yet, the first run makes it"}
}

func vulkanDiag(a wineArch) Diag {
	paths := vulkanLoaderPaths(a)
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return Diag{"vulkan-loader", DiagOK, p}
		}
	}
	return Diag{"vulkan-loader", DiagFail, "not found in " + strings.Join(paths, ", ") +
		"; DXVK cannot start, so the game flickers and closes immediately"}
}

func gstreamerDiag(d distro, a wineArch) Diag {
	dirs := d.gstreamerPluginDirs(a)
	for _, dir := range dirs {
		p := filepath.Join(dir, "libgstlibav.so")
		if _, err := os.Stat(p); err == nil {
			return Diag{"gstreamer-codecs", DiagOK, p}
		}
	}
	// Warn, not fail: the intro is cosmetic and skippable, so this must not read
	// as the reason a launch failed when something else is actually wrong.
	return Diag{"gstreamer-codecs", DiagWarn, "libgstlibav.so not found in " + strings.Join(dirs, ", ") +
		"; the intro videos fail to decode and the game closes just after it opens"}
}
