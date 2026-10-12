package main

import (
	"strings"
	"testing"

	"wc3-launcher/internal/client"
)

// The doctor report is written to be pasted into a chat window, so the relay
// token must never appear in it. This is the check that keeps a future "just
// print the config" convenience from quietly turning the support command into a
// credential leak.
func TestConfigDiagsNeverPrintsTheToken(t *testing.T) {
	const secret = "s3cret-relay-token-value"

	defer restoreConn(serverHost, relayToken, relayCertPin, gatewayName)()
	serverHost, relayToken, relayCertPin, gatewayName = "wc3.example.net", secret, "cGluCg==", "Example"

	for _, d := range configDiags() {
		if strings.Contains(d.Detail, secret) {
			t.Fatalf("doctor report leaked the relay token in %q: %s", d.Name, d.Detail)
		}
	}
}

// An unset token has to be reported as a warning rather than silently omitted:
// hosting fails without it, and a missing line reads as "fine".
func TestConfigDiagsFlagsMissingToken(t *testing.T) {
	defer restoreConn(serverHost, relayToken, relayCertPin, gatewayName)()
	serverHost, relayToken, relayCertPin, gatewayName = "wc3.example.net", "", "cGluCg==", "Example"

	if got := findDiag(t, configDiags(), "relay-token").Status; got != client.DiagWarn {
		t.Fatalf("relay-token status = %v, want warn when the token is unset", got)
	}
}

// A placeholder server is the state a stock public build ships in, and it is a
// hard failure: nothing can connect. It must not read as merely informational.
func TestConfigDiagsFailsOnPlaceholderServer(t *testing.T) {
	defer restoreConn(serverHost, relayToken, relayCertPin, gatewayName)()
	serverHost, relayToken, relayCertPin, gatewayName = placeholderHost, "t", "p", "Example"

	if got := findDiag(t, configDiags(), "server").Status; got != client.DiagFail {
		t.Fatalf("server status = %v, want fail for the built-in placeholder", got)
	}
}

func findDiag(t *testing.T, ds []client.Diag, name string) client.Diag {
	t.Helper()
	for _, d := range ds {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no %q line in the report", name)
	return client.Diag{}
}

func restoreConn(host, token, pin, gw string) func() {
	return func() { serverHost, relayToken, relayCertPin, gatewayName = host, token, pin, gw }
}
