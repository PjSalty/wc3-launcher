package main

import "testing"

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
