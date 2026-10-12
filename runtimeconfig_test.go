package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "third", "fourth"); got != "third" {
		t.Fatalf("firstNonEmpty = %q, want third", got)
	}
	if got := firstNonEmpty("flag", "env"); got != "flag" {
		t.Fatalf("firstNonEmpty = %q, want flag (precedence is arg order)", got)
	}
	if got := firstNonEmpty("", "", ""); got != "" {
		t.Fatalf("firstNonEmpty all-empty = %q, want empty", got)
	}
}

// TestResolveConnectionPrecedence proves flag > env > compiled-in default. The
// config-file layer is exercised by the JSON tag round-trip below; here the test
// binary has no wc3-launcher.json next to it, so the file layer is empty.
func TestResolveConnectionPrecedence(t *testing.T) {
	// Sandbox the per-user config dir so a real seeded config on the dev box does
	// not feed into resolveConnection's file layer and skew the precedence check.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	origServer, origToken, origPin := serverHost, relayToken, relayCertPin
	t.Cleanup(func() { serverHost, relayToken, relayCertPin = origServer, origToken, origPin })

	// Flag beats env beats compiled-in default.
	serverHost = "compiled-default"
	t.Setenv("WC3_SERVER", "from-env")
	resolveConnection("from-flag", "", "", "")
	if serverHost != "from-flag" {
		t.Fatalf("serverHost = %q, want from-flag (flag wins)", serverHost)
	}

	// Env beats the compiled-in default when no flag is given.
	relayToken = "compiled-token"
	t.Setenv("WC3_RELAY_TOKEN", "env-token")
	resolveConnection("", "", "", "")
	if relayToken != "env-token" {
		t.Fatalf("relayToken = %q, want env-token (env wins over default)", relayToken)
	}

	// Nothing set keeps the compiled-in default untouched.
	relayCertPin = "compiled-pin"
	resolveConnection("", "", "", "")
	if relayCertPin != "compiled-pin" {
		t.Fatalf("relayCertPin = %q, want compiled-pin (default preserved)", relayCertPin)
	}
}

// TestShortcutInstallOnce proves the desktop icon is a one-time thing: before
// the marker it reports not-installed, after markShortcutsInstalled it reports
// installed (so later launches skip recreating it, even if the player deletes
// the icon). XDG_CONFIG_HOME keeps the real ~/.config untouched.
func TestShortcutInstallOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if shortcutsInstalled() {
		t.Fatal("shortcutsInstalled true before any install")
	}
	markShortcutsInstalled()
	if !shortcutsInstalled() {
		t.Fatal("shortcutsInstalled false after markShortcutsInstalled")
	}
	// Deleting the icon must NOT reset the marker: a second launch stays a no-op.
	if _, err := os.Stat(filepath.Join(dir, "wc3-launcher", "shortcuts-installed")); err != nil {
		t.Fatalf("marker file missing after install: %v", err)
	}
}

// TestSeedPerUserConfig proves the first-run seed: a preconfigured build persists
// its settings (including the relay token) to the per-user config so the stable
// copy the desktop shortcut points at keeps working, while a placeholder build
// writes nothing and an existing config is never clobbered. XDG_CONFIG_HOME
// redirects os.UserConfigDir into a temp dir so the real ~/.config is untouched.
func TestSeedPerUserConfig(t *testing.T) {
	origS, origT, origP, origG := serverHost, relayToken, relayCertPin, gatewayName
	t.Cleanup(func() { serverHost, relayToken, relayCertPin, gatewayName = origS, origT, origP, origG })

	read := func(t *testing.T, base string) fileConfig {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(base, "wc3-launcher", "config.json"))
		if err != nil {
			t.Fatalf("reading seeded config: %v", err)
		}
		var c fileConfig
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatalf("unmarshaling seeded config: %v", err)
		}
		return c
	}

	// 1. A placeholder (stock public) build is not configured: nothing is written.
	dir1 := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir1)
	serverHost, relayToken, relayCertPin, gatewayName = placeholderHost, "", "", "PvPGN"
	seedPerUserConfig()
	if _, err := os.Stat(filepath.Join(dir1, "wc3-launcher", "config.json")); !os.IsNotExist(err) {
		t.Fatal("placeholder build must not seed a per-user config")
	}

	// 2. A configured build with no existing file seeds the full config, token included.
	dir2 := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir2)
	serverHost, relayToken, relayCertPin, gatewayName = "realm.example.net", "tok123", "pin==", "MyRealm"
	seedPerUserConfig()
	got := read(t, dir2)
	if got.Server != "realm.example.net" || got.Token != "tok123" || got.CertPin != "pin==" || got.Gateway != "MyRealm" {
		t.Fatalf("seeded config = %+v, want all four fields persisted", got)
	}

	// 3. An existing per-user config is never clobbered by a later run.
	serverHost, relayToken = "different", "different-token"
	seedPerUserConfig()
	if got := read(t, dir2); got.Token != "tok123" {
		t.Fatalf("seed clobbered an existing config: token=%q, want tok123", got.Token)
	}
}

// A new download ships a new wc3-launcher.json next to the binary. It won for
// that run, but the per-user copy was only ever written once, so the desktop
// shortcut (a copy of the binary with no file next to it) kept the old token and
// pin after a rotation.
func TestSeedFollowsARotatedShippedConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"WC3_SERVER", "WC3_RELAY_TOKEN", "WC3_RELAY_CERT_PIN", "WC3_GATEWAY"} {
		t.Setenv(k, "")
	}
	origS, origT, origP, origG := serverHost, relayToken, relayCertPin, gatewayName
	t.Cleanup(func() { serverHost, relayToken, relayCertPin, gatewayName = origS, origT, origP, origG })
	stock := func() { serverHost, relayToken, relayCertPin, gatewayName = placeholderHost, "", "", "PvPGN" }

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shipped := filepath.Join(filepath.Dir(exe), "wc3-launcher.json")
	ship := func(c fileConfig) {
		t.Helper()
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(shipped, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { os.Remove(shipped) })

	// The first download, run once from its folder.
	ship(fileConfig{Server: "realm.example.net", Token: "old-token", CertPin: "old-pin"})
	stock()
	resolveConnection("", "", "", "")
	seedPerUserConfig()

	// The operator rotates the token and certificate and sends a new download.
	ship(fileConfig{Server: "realm.example.net", Token: "new-token", CertPin: "new-pin"})
	stock()
	resolveConnection("", "", "", "")
	seedPerUserConfig()

	// The desktop shortcut: the same binary with nothing next to it.
	if err := os.Remove(shipped); err != nil {
		t.Fatal(err)
	}
	stock()
	resolveConnection("", "", "", "")
	if relayToken != "new-token" || relayCertPin != "new-pin" {
		t.Fatalf("the shortcut resolved token=%q pin=%q, want the rotated values", relayToken, relayCertPin)
	}

	// With nothing shipped, a later run still leaves the player's file alone.
	serverHost, relayToken = "elsewhere.example.org", "other"
	seedPerUserConfig()
	stock()
	resolveConnection("", "", "", "")
	if serverHost != "realm.example.net" || relayToken != "new-token" {
		t.Fatalf("a run with no shipped file rewrote the saved config: server=%q token=%q", serverHost, relayToken)
	}
}

func TestSaveFileConfigTightensAnExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not meaningful on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := saveFileConfig(fileConfig{Server: "realm.example.net", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := saveFileConfig(fileConfig{Server: "realm.example.net", Token: "t2"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode after a rewrite = %v, want 0600", got)
	}
}
