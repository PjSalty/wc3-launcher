package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
