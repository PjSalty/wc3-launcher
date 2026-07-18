package main

import (
	"fmt"
	"os"
)

// Bundle and connection configuration for the launcher.
//
// The server's address is NOT here: it is serverHost in version.go, injected at
// build time so no endpoint ships in source. Players connect to it on TCP 6112,
// which is hardwired in the WC3 client and cannot be changed, and to relayPort
// for the host tunnel. Both ports are forwarded to the server.
const (
	// gatewayTimezone is the server's UTC offset in hours. WC3 only uses it to
	// sort the gateway list; -6 is US Central.
	gatewayTimezone = "-6"

	// bundleVersion gates re-extraction of the embedded loader overlay: bump it
	// when the embedded assets/wc3-bundle.zip changes. v2: the 1.27 3-file
	// W3L loader (w3l.exe + w3lh.dll + wl27.dll) - the 2-file 1.25b build could
	// not patch a 1.27a war3.exe ("bad offset").
	bundleVersion = "3"

	// loaderExe starts the game through the PvPGN-compatible loader. Stock WC3
	// refuses to connect to PvPGN without it.
	loaderExe = "w3l.exe"

	// gameSubdir is the portable game folder created next to the launcher.
	gameSubdir = "WC3"

	// blizzardInstallerURL is Blizzard's OFFICIAL free legacy downloader. The
	// launcher fetches the client from Blizzard's own endpoint (they are the
	// distributor); it does not redistribute the game itself. Product code and
	// locale (WC3_LOCALE) are filled per install step. Frozen Throne is an
	// expansion, so Reign of Chaos (the base game) must be installed first.
	blizzardInstallerURL = "https://us.battle.net/download/getLegacy?product=%s&locale=%s&os=WIN"
	defaultLocale        = "enUS"

	// Blizzard legacy product codes (verified against getLegacy): WAR3 is
	// Warcraft III: Reign of Chaos (base game), W3XP is The Frozen Throne.
	productReignOfChaos = "WAR3"
	productFrozenThrone = "W3XP"

	// --- Native-host relay mode ---

	// relayPort is the relay daemon's tunnel port on serverHost.
	relayPort = "7000"

	// hostGatewayHost points WC3's Battle.net gateway at the local BnetGateway so
	// the host's realm session rides the relay (which pins the game address).
	hostGatewayHost = "127.0.0.1"

	// hostGateAddr is where the launcher's local BnetGateway listens; WC3 connects
	// here (its hardwired bnet port 6112).
	hostGateAddr = "127.0.0.1:6112"

	// hostGamePort moves WC3's own game listener off 6112 so it does not clash
	// with the local gateway on 6112; the relay bridges joiners to this port.
	hostGamePort = 6119
)

// blizzardInstaller returns the official Blizzard legacy downloader URL for the
// given product code, at the configured locale (WC3_LOCALE, default enUS).
func blizzardInstaller(product string) string {
	loc := os.Getenv("WC3_LOCALE")
	if loc == "" {
		loc = defaultLocale
	}
	return fmt.Sprintf(blizzardInstallerURL, product, loc)
}
