//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// EnsureShortcut creates Desktop and Start Menu .lnk shortcuts pointing at
// launcherPath, using the game's own icon. It overwrites existing shortcuts so
// the target stays current across launcher updates.
func EnsureShortcut(launcherPath, gameRoot string) error {
	icon := filepath.Join(gameRoot, "Frozen Throne.exe")
	if _, err := os.Stat(icon); err != nil {
		// Fall back to the base game exe, then the launcher itself.
		alt := filepath.Join(gameRoot, "Warcraft III.exe")
		if _, err := os.Stat(alt); err == nil {
			icon = alt
		} else {
			icon = launcherPath
		}
	}

	var targets []string
	if d := desktopDir(); d != "" {
		targets = append(targets, filepath.Join(d, shortcutName+".lnk"))
	}
	if s := startMenuDir(); s != "" {
		targets = append(targets, filepath.Join(s, shortcutName+".lnk"))
	}
	if len(targets) == 0 {
		return fmt.Errorf("no Desktop or Start Menu location found")
	}
	for _, lnk := range targets {
		if err := createLnk(lnk, launcherPath, icon); err != nil {
			return err
		}
	}
	return nil
}

func desktopDir() string {
	if u := os.Getenv("USERPROFILE"); u != "" {
		return filepath.Join(u, "Desktop")
	}
	return ""
}

func startMenuDir() string {
	if a := os.Getenv("APPDATA"); a != "" {
		dir := filepath.Join(a, `Microsoft\Windows\Start Menu\Programs`)
		_ = os.MkdirAll(dir, 0o755)
		return dir
	}
	return ""
}

// createLnk builds a .lnk via the WScript.Shell COM object through PowerShell,
// which is present on every supported Windows and needs no extra dependency.
func createLnk(lnkPath, target, icon string) error {
	ps := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut(" + psQuote(lnkPath) + ");" +
		"$s.TargetPath=" + psQuote(target) + ";" +
		"$s.IconLocation=" + psQuote(icon) + ";" +
		"$s.WorkingDirectory=" + psQuote(filepath.Dir(target)) + ";" +
		"$s.Description='Play Warcraft III on a PvPGN server';" +
		"$s.Save()"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ps)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create shortcut %s: %w: %s", filepath.Base(lnkPath), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// psQuote wraps s as a PowerShell single-quoted literal (doubling embedded
// quotes), so spaces and special characters in paths are safe.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// gameExeNames are the Warcraft III executables a stock shortcut points at.
var gameExeNames = []string{"war3.exe", "Warcraft III.exe", "Frozen Throne.exe", "Warcraft III Launcher.exe"}

// RepointGameShortcuts finds existing Warcraft III shortcuts (the ones Blizzard's
// installer created on the Desktop and in the Start Menu) and aims them at our
// launcher instead of the raw game.
//
// Why: launching the game directly skips the launcher, so the realm gateway is
// never configured and the relay tunnel is never up. The player lands on a
// Battle.net screen that cannot connect, with no hint why. Pointing every
// Warcraft III icon at the launcher means there is no wrong door: whichever icon
// they click, the setup runs and the game comes up ready to host.
//
// Only shortcuts whose target is actually a Warcraft III executable are touched,
// and the icon is preserved so they still look like the game. Best-effort.
func RepointGameShortcuts(launcherPath, gameRoot string) (int, error) {
	var names []string
	for _, n := range gameExeNames {
		names = append(names, psQuote(n))
	}
	ps := "$ErrorActionPreference='SilentlyContinue';" +
		"$sh=New-Object -ComObject WScript.Shell;" +
		"$targets=@(" + strings.Join(names, ",") + ");" +
		"$launcher=" + psQuote(launcherPath) + ";" +
		"$n=0;" +
		"$dirs=@([Environment]::GetFolderPath('Desktop'),[Environment]::GetFolderPath('CommonDesktopDirectory'),[Environment]::GetFolderPath('StartMenu'),[Environment]::GetFolderPath('CommonStartMenu'));" +
		"foreach($d in $dirs){ if(-not $d -or -not (Test-Path $d)){continue};" +
		"Get-ChildItem -Path $d -Filter *.lnk -Recurse | ForEach-Object {" +
		"$l=$sh.CreateShortcut($_.FullName);" +
		"if(-not $l.TargetPath){return};" +
		"$leaf=Split-Path $l.TargetPath -Leaf;" +
		"if(($targets -contains $leaf) -and ($l.TargetPath -ne $launcher)){" +
		"if(-not $l.IconLocation -or $l.IconLocation -eq ',0'){$l.IconLocation=$l.TargetPath};" +
		"$l.TargetPath=$launcher; $l.Arguments=''; $l.Save(); $n++ } } };" +
		"Write-Output $n"
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ps)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("repoint game shortcuts: %w: %s", err, strings.TrimSpace(string(out)))
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n, nil
}
