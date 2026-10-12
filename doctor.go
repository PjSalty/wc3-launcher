package main

// The `doctor` subcommand: one command a player can run that prints everything
// support would otherwise have to ask for.
//
// It exists because diagnosing one Linux install took three round trips of
// "run this, paste the output" - each one a day of latency - and every question
// asked was something the launcher already knew. The report is plain text so it
// pastes into a chat window intact.
//
// Two hard rules:
//   - READ-ONLY. It installs nothing, prompts for nothing, and writes no files,
//     so it is safe to run when the launcher itself will not start.
//   - It NEVER prints the relay token. The cert pin is printed because it is
//     public by design (it is the hash of a public key, and comparing it is how
//     you tell a stale build from a rotated certificate).

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"wc3-launcher/internal/client"
	"wc3-launcher/internal/installer"
	"wc3-launcher/internal/maplib"
)

// doctorTimeout bounds every network probe. The report must always finish: a
// player runs this precisely when something is unreachable, so a hang here
// would be the second thing that fails them.
const doctorTimeout = 6 * time.Second

// runDoctor prints the full report and returns nil. It deliberately does not
// return an error for failed checks: the report IS the output, and a non-zero
// exit would make the launcher's error path bury it behind "Something went
// wrong".
func runDoctor() error {
	fmt.Println("=== Warcraft III Launcher - doctor ===")
	fmt.Println(versionBanner())
	fmt.Printf("Built for %s/%s, running on %s\n\n", runtime.GOOS, runtime.GOARCH, runtime.GOOS)

	dir, dirErr := gameDir()

	var out []client.Diag
	out = append(out, configDiags()...)
	out = append(out, gameDiags(dir, dirErr)...)
	out = append(out, client.HostDiagnostics(dir)...)
	out = append(out, networkDiags()...)

	printDiags(out)
	printSummary(out)
	return nil
}

// printDiags renders the report in a fixed-width column layout so a pasted copy
// stays readable even when a chat client eats the formatting.
func printDiags(ds []client.Diag) {
	width := 0
	for _, d := range ds {
		if len(d.Name) > width {
			width = len(d.Name)
		}
	}
	for _, d := range ds {
		fmt.Printf("[%-4s] %-*s  %s\n", d.Status, width, d.Name, d.Detail)
	}
}

func printSummary(ds []client.Diag) {
	var fails, warns int
	for _, d := range ds {
		switch d.Status {
		case client.DiagFail:
			fails++
		case client.DiagWarn:
			warns++
		}
	}
	fmt.Println()
	switch {
	case fails > 0:
		fmt.Printf("%d problem(s) that stop the game from running, %d warning(s).\n", fails, warns)
		fmt.Println("The FAIL lines above say what to do. If they don't help, paste this whole report to whoever gave you the launcher.")
	case warns > 0:
		fmt.Printf("No blocking problems, %d warning(s). The game should run.\n", warns)
	default:
		fmt.Println("Everything checks out.")
	}
}

// configDiags reports the resolved connection settings. Resolution has four
// layers (flag, env, config file, compiled-in default), and "which one won" is
// a question that has come up on its own.
func configDiags() []client.Diag {
	var ds []client.Diag

	if !isConfigured() {
		ds = append(ds, client.Diag{Name: "server", Status: client.DiagFail,
			Detail: "not configured (still the built-in placeholder). Set it with --server, the WC3_SERVER env var, or a wc3-launcher.json next to the binary."})
	} else {
		ds = append(ds, client.Diag{Name: "server", Status: client.DiagOK, Detail: serverHost})
	}
	ds = append(ds, client.Diag{Name: "gateway-name", Status: client.DiagInfo, Detail: gatewayName})

	// Presence only, never the value: this report is written to be pasted into a
	// chat window.
	if relayToken == "" {
		ds = append(ds, client.Diag{Name: "relay-token", Status: client.DiagWarn,
			Detail: "not set; hosting your own games will not work (joining still will)"})
	} else {
		ds = append(ds, client.Diag{Name: "relay-token", Status: client.DiagOK, Detail: "set (value not shown)"})
	}

	// The pin is a hash of a public key, so printing it is safe and useful: it is
	// how a stale build gets told apart from a rotated relay certificate.
	if relayCertPin == "" {
		ds = append(ds, client.Diag{Name: "relay-cert-pin", Status: client.DiagWarn,
			Detail: "not set; the relay certificate is checked against system roots instead of being pinned"})
	} else {
		ds = append(ds, client.Diag{Name: "relay-cert-pin", Status: client.DiagOK, Detail: relayCertPin})
	}

	found := false
	for _, p := range configPaths() {
		if _, err := os.Stat(p); err == nil {
			ds = append(ds, client.Diag{Name: "config-file", Status: client.DiagOK, Detail: p})
			found = true
			break
		}
	}
	if !found {
		ds = append(ds, client.Diag{Name: "config-file", Status: client.DiagInfo,
			Detail: "none on disk; using built-in or flag/env values (looked in " + strings.Join(configPaths(), ", ") + ")"})
	}
	return ds
}

// gameDiags reports what is actually installed on disk.
func gameDiags(dir string, dirErr error) []client.Diag {
	if dirErr != nil {
		return []client.Diag{{Name: "game-folder", Status: client.DiagFail, Detail: "could not resolve: " + dirErr.Error()}}
	}
	ds := []client.Diag{{Name: "game-folder", Status: client.DiagInfo, Detail: dir}}

	gameRoot, ok := installer.InstallPath(dir)
	if !ok {
		ds = append(ds, client.Diag{Name: "warcraft-iii", Status: client.DiagWarn,
			Detail: "not installed yet; the launcher installs it from Blizzard's official free download on first run"})
		return ds
	}
	ds = append(ds, client.Diag{Name: "warcraft-iii", Status: client.DiagOK, Detail: gameRoot})

	if installer.HasExpansion(gameRoot) {
		ds = append(ds, client.Diag{Name: "frozen-throne", Status: client.DiagOK, Detail: "expansion present (War3x.mpq)"})
	} else {
		ds = append(ds, client.Diag{Name: "frozen-throne", Status: client.DiagWarn,
			Detail: "expansion missing; the launcher installs it on the next run"})
	}

	// The loader is what makes a stock client talk to PvPGN at all, so its
	// absence is a hard failure rather than a warning.
	loader := filepath.Join(gameRoot, loaderExe)
	if _, err := os.Stat(loader); err == nil {
		ds = append(ds, client.Diag{Name: "pvpgn-loader", Status: client.DiagOK, Detail: loader})
	} else {
		ds = append(ds, client.Diag{Name: "pvpgn-loader", Status: client.DiagFail,
			Detail: loader + " missing; a stock client cannot connect to the realm without it"})
	}

	// Map count, because "why can't I see the map everyone else has" is a
	// question that looks like a bug and is usually an out-of-date sync.
	mapsDir := filepath.Join(gameRoot, "Maps", "Download")
	entries, err := os.ReadDir(mapsDir)
	if err != nil {
		ds = append(ds, client.Diag{Name: "synced-maps", Status: client.DiagInfo,
			Detail: mapsDir + " not present yet; it is created by the first map sync"})
		return ds
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && maplib.IsMapFile(e.Name()) {
			n++
		}
	}
	ds = append(ds, client.Diag{Name: "synced-maps", Status: client.DiagOK, Detail: fmt.Sprintf("%d map(s) in %s", n, mapsDir)})
	return ds
}

// networkDiags probes the three services a player depends on, each on the port
// it really uses. They fail independently and for different reasons, so they
// are reported separately rather than as one "server reachable" line.
func networkDiags() []client.Diag {
	if !isConfigured() {
		return []client.Diag{{Name: "network", Status: client.DiagInfo, Detail: "skipped: no server configured"}}
	}
	return []client.Diag{realmDiag(), relayDiag(), mapsDiag()}
}

// realmDiag checks PvPGN on 6112. That port is hardwired in the WC3 client and
// cannot be moved, so it is the one that must be open.
func realmDiag() client.Diag {
	addr := net.JoinHostPort(serverHost, "6112")
	start := time.Now()
	c, err := net.DialTimeout("tcp", addr, doctorTimeout)
	if err != nil {
		return client.Diag{Name: "realm", Status: client.DiagFail,
			Detail: fmt.Sprintf("%s unreachable (%v); you cannot log in to the realm", addr, err)}
	}
	_ = c.Close()
	return client.Diag{Name: "realm", Status: client.DiagOK,
		Detail: fmt.Sprintf("%s reachable in %s", addr, time.Since(start).Round(time.Millisecond))}
}

// relayDiag does a real TLS handshake, not a bare connect. The relay is
// TLS-only, so a plain connect proves the socket is bound and nothing more, and
// the handshake is also the only way to see the certificate expiry - which,
// because the launcher pins it, would break every player at once.
func relayDiag() client.Diag {
	addr := net.JoinHostPort(serverHost, relayPort)
	d := &net.Dialer{Timeout: doctorTimeout}
	c, err := tls.DialWithDialer(d, "tcp", addr, relayTLSConfig(serverHost))
	if err != nil {
		// A pin mismatch is a completely different problem from an unreachable
		// relay, and telling them apart is most of the value of this check.
		if strings.Contains(err.Error(), "pin mismatch") {
			return client.Diag{Name: "host-relay", Status: client.DiagFail,
				Detail: addr + " presented a certificate that does not match this build's pin. Either the relay certificate was replaced and this launcher is out of date, or the connection is being intercepted. Hosting will not work."}
		}
		return client.Diag{Name: "host-relay", Status: client.DiagWarn,
			Detail: fmt.Sprintf("%s unreachable (%v); you can still join games, but not host them", addr, err)}
	}
	defer c.Close()

	certs := c.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return client.Diag{Name: "host-relay", Status: client.DiagWarn, Detail: addr + " connected but presented no certificate"}
	}
	left := time.Until(certs[0].NotAfter)
	detail := fmt.Sprintf("%s reachable, certificate valid for %d more day(s)", addr, int(left.Hours()/24))
	if left < 14*24*time.Hour {
		return client.Diag{Name: "host-relay", Status: client.DiagWarn,
			Detail: detail + " - it is pinned, so hosting breaks for everyone when it expires"}
	}
	return client.Diag{Name: "host-relay", Status: client.DiagOK, Detail: detail}
}

// mapsDiag fetches the manifest the launcher itself reads. A map-sync failure
// is silent by design (it never blocks play), which is exactly why it needs to
// show up here.
func mapsDiag() client.Diag {
	base := mapsBaseURL()
	if base == "" {
		return client.Diag{Name: "map-service", Status: client.DiagInfo, Detail: "skipped: no server configured"}
	}
	url := base + "/manifest.json"
	hc := &http.Client{
		Timeout:   doctorTimeout,
		Transport: &http.Transport{TLSClientConfig: relayTLSConfig(serverHost)},
	}
	resp, err := hc.Get(url)
	if err != nil {
		return client.Diag{Name: "map-service", Status: client.DiagWarn,
			Detail: fmt.Sprintf("%s unreachable (%v); your maps will not update, but you can still play", url, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return client.Diag{Name: "map-service", Status: client.DiagWarn,
			Detail: fmt.Sprintf("%s returned HTTP %d; your maps will not update", url, resp.StatusCode)}
	}
	var m maplib.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return client.Diag{Name: "map-service", Status: client.DiagWarn,
			Detail: fmt.Sprintf("%s returned something that is not a manifest (%v)", url, err)}
	}
	return client.Diag{Name: "map-service", Status: client.DiagOK,
		Detail: fmt.Sprintf("%s serving %d map(s)", url, len(m.Maps))}
}
