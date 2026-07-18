//go:build linux

package client

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestConfigureImportsGateway_Wine runs the real Configure against a throwaway
// Wine prefix and reads the value back with `wine reg query`, proving the
// generated .reg round-trips. Skipped unless Wine is installed and
// WC3_WINE_IT=1, so it never runs (or flakes, prefix creation is slow) in
// normal CI.
func TestConfigureImportsGateway_Wine(t *testing.T) {
	if os.Getenv("WC3_WINE_IT") != "1" {
		t.Skip("set WC3_WINE_IT=1 to run the Wine integration test")
	}
	if _, err := exec.LookPath("wine"); err != nil {
		t.Skip("wine not installed")
	}

	dir := t.TempDir()
	if err := Configure(dir, "wc3.example.com", "PvPGN", "-6"); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	cmd := exec.Command("wine", "reg", "query", gatewayRegKey, "/v", gatewayValueName)
	cmd.Env = wineEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reg query: %v\n%s", err, out)
	}
	// wine renders REG_MULTI_SZ with a literal "\0" between the UTF-16 high and
	// low bytes of every character; strip those to recover the plain text.
	normalized := strings.ReplaceAll(string(out), `\0`, "")
	if !strings.Contains(normalized, "wc3.example.com") {
		t.Fatalf("gateway value missing our host; got:\n%s", out)
	}
	if !strings.Contains(normalized, "PvPGN") {
		t.Fatalf("gateway value missing our name; got:\n%s", out)
	}
}
