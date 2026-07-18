package main

import "fmt"

// launcherVersion is the launcher's own build version. It is injected
// at release time with -ldflags "-X main.launcherVersion=v1.2.3" (the release CI
// sets it from the git tag); local builds report "dev".
var launcherVersion = "dev"

// serverHost is the realm/relay host the launcher talks to: the PvPGN gateway
// (TCP 6112, hardwired in the WC3 client) and the relay tunnel (relayPort).
// It is injected at build time with -ldflags "-X main.serverHost=..." and
// defaults to a placeholder in source, so no server's address ships in the repo.
// Publishing an endpoint invites everyone who reads the source to point traffic
// at it; the people who should reach it get a built binary instead.
//
// A build with the default cannot reach a real server: set it, or the launcher
// tries to connect to the placeholder and fails. The release build injects it.
const placeholderHost = "wc3.example.com"

var serverHost = placeholderHost

// relayToken is the shared tunnel token presented to the relay. It is injected
// at build time with -ldflags "-X main.relayToken=..." and is intentionally
// EMPTY in source, so the public repo never contains it. It gates "must be our
// launcher"; pvpgn's account login is the real play-access auth.
var relayToken = ""

// relayCertPin is the base64 SHA-256 of the relay certificate's
// SubjectPublicKeyInfo, injected at build time with
// -ldflags "-X main.relayCertPin=...". When set, the launcher pins the relay's
// TLS certificate to this value (defeats MITM without needing a public CA, since
// the relay uses a self-signed certificate). It is public information, safe to
// ship in the binary. Empty falls back to standard system-root verification.
var relayCertPin = ""

const (
	// wc3ReignVersion and wc3FrozenVersion are the Warcraft III patch levels the
	// launcher installs and the realm requires. Both classic products
	// patch to the same 1.28.5 level; they are two names so a future split (if the
	// base game and expansion ever diverge) is a one-line change.
	wc3ReignVersion  = "1.28.5"
	wc3FrozenVersion = "1.28.5"
)

// versionBanner is the one-line version stamp shown at startup and by --version,
// so a player (and support) can see exactly what the launcher runs: the launcher
// build, both Warcraft III patch levels, and the bundled loader+maps version.
func versionBanner() string {
	return fmt.Sprintf("Launcher %s  |  Reign of Chaos %s  |  Frozen Throne %s  |  loader+maps bundle v%s",
		launcherVersion, wc3ReignVersion, wc3FrozenVersion, bundleVersion)
}
