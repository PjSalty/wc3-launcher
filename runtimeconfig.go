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
	Gateway string `json:"gateway"`
}

// perUserConfigPath is the stable per-user config file
// (%AppData%\wc3-launcher\config.json on Windows, ~/.config/wc3-launcher/... on
// Linux). It is the second place loadFileConfig looks and the file saveFileConfig
// writes, so both derive it from here.
func perUserConfigPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "wc3-launcher", "config.json"), nil
}

// configPaths lists where loadFileConfig looks, in order: next to the binary
// first (drop a file next to the exe and run), then the per-user config dir.
func configPaths() []string {
	var paths []string
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), "wc3-launcher.json"))
	}
	if p, err := perUserConfigPath(); err == nil {
		paths = append(paths, p)
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

// saveFileConfig writes cfg to the per-user config dir and returns the path. It
// deliberately does NOT write next to the binary: a stable per-user location
// (%AppData%\wc3-launcher on Windows, ~/.config/wc3-launcher on Linux) means a
// player can download a newer launcher into any folder and it picks the config
// straight back up, so nobody re-enters their server after the first time. Mode
// 0600 (dir 0700) because the token, if present, is mildly sensitive.
func saveFileConfig(cfg fileConfig) (string, error) {
	path, err := perUserConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
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
func resolveConnection(serverFlag, tokenFlag, certPinFlag, gatewayFlag string) {
	fc := loadFileConfig()
	serverHost = firstNonEmpty(serverFlag, os.Getenv("WC3_SERVER"), fc.Server, serverHost)
	relayToken = firstNonEmpty(tokenFlag, os.Getenv("WC3_RELAY_TOKEN"), fc.Token, relayToken)
	relayCertPin = firstNonEmpty(certPinFlag, os.Getenv("WC3_RELAY_CERT_PIN"), fc.CertPin, relayCertPin)
	gatewayName = firstNonEmpty(gatewayFlag, os.Getenv("WC3_GATEWAY"), fc.Gateway, gatewayName)
}

// isConfigured reports whether a real server has been set (i.e. this is not a
// stock public build still pointing at the placeholder).
func isConfigured() bool {
	return serverHost != "" && serverHost != placeholderHost
}

// seedPerUserConfig copies the active connection settings into the per-user
// config on first run, so a relaunch from the stable copy (or a later download
// into a new folder) keeps working without re-entering anything. A preconfigured
// build ships wc3-launcher.json next to the binary, but that file is only read
// from the folder it sits in; ensureStableLauncher aims the desktop/Start-Menu
// shortcuts at a stable copy elsewhere, which would otherwise see no config and
// re-prompt for a token the player does not have. The per-user file is read from
// any location, closing that gap.
//
// It writes only when a real server is configured and no per-user config exists
// yet, so it never clobbers a file the player already has, and a next-to-binary
// wc3-launcher.json still wins while present (higher precedence in configPaths).
func seedPerUserConfig() {
	if !isConfigured() {
		return
	}
	path, err := perUserConfigPath()
	if err != nil {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return // already present: leave the player's file untouched
	}
	// shortcut: best-effort seed; a write failure just means the next run retries.
	_, _ = saveFileConfig(fileConfig{Server: serverHost, Token: relayToken, CertPin: relayCertPin, Gateway: gatewayName})
}

// mapsBaseURL is where the launcher syncs the curated map library from. It is
// derived from the configured server (https://<serverHost>:<mapsPort>) so there
// is nothing extra to set up, with WC3_MAPS_URL as an override for an unusual
// layout. Empty when no real server is configured (nothing to sync from).
func mapsBaseURL() string {
	if v := strings.TrimRight(os.Getenv("WC3_MAPS_URL"), "/"); v != "" {
		return v
	}
	if !isConfigured() {
		return ""
	}
	return fmt.Sprintf("https://%s:%s", serverHost, mapsPort)
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
	gateway := ask(in, "  Realm name shown in-game (leave blank for the default): ")

	cfg := fileConfig{Server: server, Token: token, CertPin: certPin, Gateway: gateway}
	if path, err := saveFileConfig(cfg); err != nil {
		fmt.Printf("  (could not save config, using these for this run only: %v)\n", err)
	} else {
		fmt.Printf("  Saved to %s. Delete that file to run setup again.\n", path)
	}
	serverHost = server
	relayToken = firstNonEmpty(token, relayToken)
	relayCertPin = firstNonEmpty(certPin, relayCertPin)
	gatewayName = firstNonEmpty(gateway, gatewayName)
}

// adoptExistingGateway migrates a player upgrading from an older, baked-in
// launcher: it takes the server and realm name that launcher already wrote into
// the game's registry, asks only for a relay token if their server needs one,
// and persists it, so the upgrade needs no full setup. The wire protocol is
// unchanged, so the adopted server keeps working with an already-deployed relay.
func adoptExistingGateway(host, name string) {
	serverHost = host
	if name != "" {
		gatewayName = name
	}
	fmt.Printf("\nFound your existing server (%s). Migrating it so you don't set it up again.\n", host)
	if stdinIsTerminal() {
		token := ask(bufio.NewReader(os.Stdin), "  Relay token, if your server needs one (blank if not): ")
		relayToken = firstNonEmpty(token, relayToken)
	}
	cfg := fileConfig{Server: serverHost, Token: relayToken, CertPin: relayCertPin, Gateway: gatewayName}
	if path, err := saveFileConfig(cfg); err == nil {
		fmt.Printf("  Saved to %s. You won't be asked again.\n", path)
	}
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
