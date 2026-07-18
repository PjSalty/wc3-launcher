package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileConfig is an optional on-disk override for the connection settings that
// are otherwise injected at build time (serverHost, relayToken, relayCertPin in
// version.go). It lets someone point a stock binary at their own server without
// rebuilding. Every field is optional; anything empty falls through to the next
// source.
type fileConfig struct {
	Server  string `json:"server"`
	Token   string `json:"token"`
	CertPin string `json:"certPin"`
}

// configPaths lists where loadFileConfig looks, in order: next to the binary
// first (drop a file next to the exe and run), then the per-user config dir
// (%AppData%\wc3-launcher\config.json on Windows, ~/.config/wc3-launcher/... on
// Linux).
func configPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "wc3-launcher.json"))
	}
	if base, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(base, "wc3-launcher", "config.json"))
	}
	return paths
}

// loadFileConfig returns the first config file found. A missing file is not an
// error (returns the zero value); a malformed one is surfaced on stderr and
// skipped, never silently swallowed.
func loadFileConfig() fileConfig {
	for _, p := range configPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue // absent or unreadable: try the next location
		}
		var c fileConfig
		if err := json.Unmarshal(b, &c); err != nil {
			fmt.Fprintf(os.Stderr, "warning: ignoring malformed config %s: %v\n", p, err)
			continue
		}
		return c
	}
	return fileConfig{}
}

// saveFileConfig writes cfg next to the binary and returns the path. Mode 0600
// because the token, if present, is mildly sensitive.
func saveFileConfig(cfg fileConfig) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := filepath.Join(filepath.Dir(exe), "wc3-launcher.json")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// resolveConnection overlays runtime sources onto the build-injected defaults in
// serverHost / relayToken / relayCertPin. Precedence, highest first: CLI flag,
// env var, config file, then the compiled-in default. It runs once at startup,
// before anything reads those package vars, so the rest of the code keeps
// reading them exactly as before.
func resolveConnection(serverFlag, tokenFlag, certPinFlag string) {
	fc := loadFileConfig()
	serverHost = firstNonEmpty(serverFlag, os.Getenv("WC3_SERVER"), fc.Server, serverHost)
	relayToken = firstNonEmpty(tokenFlag, os.Getenv("WC3_RELAY_TOKEN"), fc.Token, relayToken)
	relayCertPin = firstNonEmpty(certPinFlag, os.Getenv("WC3_RELAY_CERT_PIN"), fc.CertPin, relayCertPin)
}

// isConfigured reports whether a real server has been set (i.e. this is not a
// stock public build still pointing at the placeholder).
func isConfigured() bool {
	return serverHost != "" && serverHost != placeholderHost
}

// maybeInteractiveSetup is the "download and just enter your server" path for a
// stock public build. When no server is configured and we are attached to a
// terminal, it asks for the server (and optional token / cert pin), saves the
// answers to a config file next to the binary so later runs skip the prompt, and
// applies them for this run. A build with the server injected, or a piped/CI
// stdin, skips this entirely.
func maybeInteractiveSetup() {
	if isConfigured() || !stdinIsTerminal() {
		return
	}
	in := bufio.NewReader(os.Stdin)
	fmt.Println("\nFirst-time setup: point this launcher at a Warcraft III PvPGN server.")
	server := ask(in, "  Server address (host or IP): ")
	if server == "" {
		return // nothing entered: fall through to the not-configured warning
	}
	token := ask(in, "  Relay token (leave blank if the server has none): ")
	certPin := ask(in, "  Relay cert pin (leave blank for standard TLS verification): ")

	cfg := fileConfig{Server: server, Token: token, CertPin: certPin}
	if path, err := saveFileConfig(cfg); err != nil {
		fmt.Printf("  (could not save config, using these for this run only: %v)\n", err)
	} else {
		fmt.Printf("  Saved to %s. Delete that file to run setup again.\n", path)
	}
	serverHost = server
	relayToken = firstNonEmpty(token, relayToken)
	relayCertPin = firstNonEmpty(certPin, relayCertPin)
}

// ask prints a label and reads one trimmed line, accepting empty input (so
// optional fields can be skipped with Enter).
func ask(in *bufio.Reader, label string) string {
	fmt.Print(label)
	line, _ := in.ReadString('\n')
	return strings.TrimSpace(line)
}

// stdinIsTerminal reports whether stdin is an interactive terminal, so setup
// only prompts a real person, never a pipe or a CI runner.
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// firstNonEmpty returns the first non-empty string, so precedence is just
// argument order.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
