// Package client configures and launches the Warcraft III client so it
// connects to a PvPGN server. It works on Windows (native
// registry, launch the loader directly) and Linux (Wine prefix registry,
// launch via Wine).
package client

import (
	"os/exec"
	"strings"
	"time"
)

// gameProcessNames are the Warcraft III game executables to watch for, so the
// launcher can tell the running game apart from the short-lived W3L loader
// (w3l.exe) and, on Linux, the Wine desktop process. Matched case-insensitively.
var gameProcessNames = []string{"war3.exe", "warcraft iii.exe", "frozen throne.exe"}

// WaitForGameExit blocks until the actual Warcraft III game process has exited.
// It deliberately does NOT treat started (the loader / Wine desktop process,
// which exits as soon as it has spawned the game) as the game itself - doing so
// tore the relay down mid-login. If the game process never appears within the
// grace window, it blocks forever so the launcher stays resident and keeps
// carrying the relay rather than closing early.
func WaitForGameExit(started *exec.Cmd) {
	// Reap the loader/desktop process in the background so it never lingers as a
	// zombie, but do NOT block on it: on Windows the loader exits instantly and on
	// Linux the Wine desktop process may live for the whole game, so neither is a
	// reliable "game closed" signal. Poll for the real game process instead.
	if started != nil {
		go func() { _ = started.Wait() }()
	}
	// Phase 1: wait for the game to appear. A first launch (with patching) can be
	// slow, so allow several minutes; until then the launcher stays resident.
	appeared := false
	for i := 0; i < 150; i++ { // ~5 minutes at 2s
		if gameRunning() {
			appeared = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !appeared {
		select {} // never detected the game; never signal exit
	}
	// Phase 2: wait for it to close. Two consecutive misses guard against a
	// transient enumeration glitch flapping the launcher shut mid-game.
	misses := 0
	for {
		time.Sleep(2 * time.Second)
		if gameRunning() {
			misses = 0
			continue
		}
		misses++
		if misses >= 2 {
			return
		}
	}
}

// gatewayRegKey is the Warcraft III registry key holding the gateway list.
const gatewayRegKey = `HKEY_CURRENT_USER\Software\Blizzard Entertainment\Warcraft III`

// gatewayValueName is the value under gatewayRegKey.
const gatewayValueName = "Battle.net Gateways"

// wc3DefaultGateways are Blizzard's classic Battle.net gateways, listed so our
// realm shows up alongside the familiar four rather than replacing them. Each
// is an {address, zone, name} triple; the zone integer only affects the
// client's latency sort, not connectivity.
var wc3DefaultGateways = [][3]string{
	{"uswest.battle.net", "8", "Lordaeron (U.S. West)"},
	{"useast.battle.net", "-5", "Azeroth (U.S. East)"},
	{"asia.battle.net", "-9", "Kalimdor (Asia)"},
	{"europe.battle.net", "1", "Northrend (Europe)"},
}

// gatewayStrings returns the REG_MULTI_SZ string list for the "Battle.net
// Gateways" value, in the exact positional layout WC3's Game.dll parser
// requires (established by disassembly + a RegQueryValueExA probe):
//
//	"1001" - version header. token[0] is atoi'd and must be >= 1001, otherwise
//	         the WHOLE list is rejected and only the built-in four are shown.
//	"0"    - selected-gateway index (0 = our realm, which we list first).
//	then repeating {address, zone, name} triples, with NO per-entry index.
//
// The value must also be stored as single-byte ANSI (see the Linux Configure):
// WC3 reads it with the ANSI registry API, so a UTF-16 value is seen as a
// one-character "w" and silently discarded.
func gatewayStrings(host, timezone, name string) []string {
	out := []string{"1001", "0", host, timezone, name}
	for _, g := range wc3DefaultGateways {
		out = append(out, g[0], g[1], g[2])
	}
	return out
}

// parseGateway pulls the first realm's host and display name out of a "Battle.net
// Gateways" REG_MULTI_SZ that a prior launcher wrote, whose layout is
// ["1001", "<selected index>", host, zone, name, <default gateways>...]. It lets
// an upgrading player be migrated off an older baked-in launcher without
// re-entering their server. ok=false if it does not look like our value, or the
// first host is a stock Blizzard gateway (nothing of ours to migrate).
func parseGateway(vals []string) (host, name string, ok bool) {
	if len(vals) < 5 || vals[0] != "1001" {
		return "", "", false
	}
	host, name = strings.TrimSpace(vals[2]), strings.TrimSpace(vals[4])
	if host == "" || strings.HasSuffix(strings.ToLower(host), "battle.net") {
		return "", "", false
	}
	return host, name, true
}
