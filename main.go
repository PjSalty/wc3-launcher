// Command wc3-launcher is the Warcraft III launcher. A friend
// downloads one binary, runs it, and it: installs Warcraft III from Blizzard's
// own official free download if it is not already present, adds the W3L loader
// and maps, points the Battle.net gateway at a PvPGN server, and launches the
// game. Runs on Windows natively and on Linux through Wine.
//
// The game client is fetched from Blizzard's official endpoint; this launcher
// never redistributes Blizzard's game files. It ships only the GPL W3L loader
// and community maps.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"wc3-launcher/internal/bundle"
	"wc3-launcher/internal/client"
	"wc3-launcher/internal/desktop"
	"wc3-launcher/internal/installer"
	"wc3-launcher/internal/mapsync"
	"wc3-launcher/internal/relaylink"
)

func main() {
	if err := run(); err != nil {
		if errors.Is(err, errAlreadyRunning) {
			fmt.Println("\nThe launcher is already running in another window.")
			fmt.Println("Close that window first, or just switch to it - you are already connected there.")
			fmt.Println("\nPress Enter to close.")
			_, _ = fmt.Scanln()
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "\n  Something went wrong: %v\n\n", err)
		fmt.Println("Press Enter to close.")
		_, _ = fmt.Scanln()
		os.Exit(1)
	}
}

func run() error {
	// Relay-host is the DEFAULT: just running the launcher makes you able to
	// Create Game and be joined, no flag and no router setup. --direct forces a
	// plain realm connection (join-only) and is only for debugging.
	directMode := flag.Bool("direct", false, "connect straight to the realm instead of the host relay (join-only)")
	showVersion := flag.Bool("version", false, "print launcher and Warcraft III versions and exit")
	serverFlag := flag.String("server", "", "PvPGN + relay host to point at (also WC3_SERVER env, or wc3-launcher.json)")
	tokenFlag := flag.String("token", "", "relay auth token (also WC3_RELAY_TOKEN env, or wc3-launcher.json)")
	certPinFlag := flag.String("cert-pin", "", "base64 SHA-256 of the relay cert SPKI (also WC3_RELAY_CERT_PIN env, or wc3-launcher.json)")
	gatewayFlag := flag.String("gateway", "", "realm display name in the WC3 gateway list (also WC3_GATEWAY env, or wc3-launcher.json)")
	flag.Parse()

	if *showVersion {
		fmt.Println(versionBanner())
		return nil
	}

	// Runtime config: --flags / env / config file override the build-injected
	// serverHost, relayToken, relayCertPin, so a stock binary can point at any
	// server without a rebuild. Nothing set falls back to the compiled-in value.
	resolveConnection(*serverFlag, *tokenFlag, *certPinFlag, *gatewayFlag)

	// Persist a preconfigured build's settings (server + token + cert pin) to the
	// per-user config on first run, so the desktop shortcut - which points at a
	// stable copy of the binary that has no wc3-launcher.json next to it - keeps
	// working instead of re-prompting for a token the player was never given.
	seedPerUserConfig()

	// Resolve the game/prefix folder up front: migration reads the realm an older
	// launcher wrote into the game's registry, so we need it before deciding
	// whether to prompt.
	dir, err := gameDir()
	if err != nil {
		return fmt.Errorf("locating game folder: %w", err)
	}

	// Nothing configured yet: first try to migrate a player upgrading from an
	// older, baked-in launcher (adopt the realm it already wrote into the game's
	// registry); otherwise run the one-time first-run prompt. Either way the
	// answer persists to the config dir, so this happens only once, even after a
	// later launcher download.
	if !isConfigured() {
		if host, name, ok := client.ExistingGateway(dir); ok {
			adoptExistingGateway(host, name)
		} else {
			maybeInteractiveSetup()
		}
	}
	if !isConfigured() {
		fmt.Println("No server is configured. Set one with --server, the WC3_SERVER env var, or a wc3-launcher.json file next to the binary (see the README). This build points at a placeholder and will not connect.")
	}

	fmt.Println("=== Warcraft III Launcher ===")
	fmt.Println(versionBanner())
	fmt.Printf("Working folder: %s\n", dir)

	// 0. On Linux, make sure Wine is available before we install or launch (no-op
	//    on Windows). Existing Wine is used untouched; if missing, we offer to
	//    install it. The dedicated Wine prefix means the user's ~/.wine is never
	//    modified.
	if err := client.EnsurePrereqs(); err != nil {
		return err
	}

	// 1. Make sure Warcraft III is installed. If not, fetch and run Blizzard's
	//    own official free installers. The Frozen Throne is an expansion, so
	//    Reign of Chaos (the base game) must be installed first, then the
	//    expansion. The client comes from Blizzard, not us.
	gameRoot, haveGame := installer.InstallPath(dir)
	haveExpansion := haveGame && installer.HasExpansion(gameRoot)
	if !haveGame || !haveExpansion {
		// Install only what is missing: Reign of Chaos (base) if absent, then the
		// Frozen Throne expansion (War3x.mpq) if absent. This avoids re-downloading
		// the base game just to add the expansion.
		var steps []struct{ product, label string }
		if !haveGame {
			steps = append(steps, struct{ product, label string }{productReignOfChaos, "Reign of Chaos (base game)"})
		}
		if !haveExpansion {
			steps = append(steps, struct{ product, label string }{productFrozenThrone, "The Frozen Throne (expansion)"})
		}
		for _, step := range steps {
			fmt.Printf("\nInstalling Warcraft III: %s ...\n", step.label)
			inst, err := installer.Download(dir, blizzardInstaller(step.product))
			if err != nil {
				return fmt.Errorf("downloading the %s installer: %w", step.label, err)
			}
			fmt.Println("Follow Blizzard's installer prompts (enter your CD key). This window continues when it finishes.")
			if err := installer.Run(dir, inst); err != nil {
				return fmt.Errorf("running the %s installer: %w", step.label, err)
			}
		}
		if gameRoot, haveGame = installer.InstallPath(dir); !haveGame {
			return fmt.Errorf("Warcraft III still not found after install; if you installed it elsewhere, this launcher cannot locate it yet")
		}
	}
	fmt.Printf("Warcraft III: %s\n", gameRoot)

	// 2. Add the W3L loader + maps (only when the bundle version changes).
	if err := bundle.Ensure(gameRoot, bundleVersion); err != nil {
		return fmt.Errorf("adding loader and maps: %w", err)
	}

	// 2c. Sync the server's curated, read-only map library into Maps/Download so
	//     everyone hosts and joins from the same set and nobody waits on an in-game
	//     map transfer. Strictly additive: it never overwrites or deletes a local
	//     map, so a player's own maps are safe. Best effort: a sync failure never
	//     blocks play, and there is no upload path (see internal/mapsync).
	if base := mapsBaseURL(); base != "" {
		mapsDir := filepath.Join(gameRoot, "Maps", "Download")
		syncCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		mapsLog := log.New(os.Stdout, "maps ", log.LstdFlags|log.Lmsgprefix)
		if n, err := mapsync.Sync(syncCtx, base, mapsDir, relayTLSConfig(serverHost), mapsLog); err != nil {
			fmt.Printf("(Map sync skipped: %v)\n", err)
		} else if n > 0 {
			fmt.Printf("Synced %d map(s) from the server.\n", n)
		}
		cancel()
	}

	// 2b. Keep a stable copy of this binary next to the game EVERY launch, so a
	//     shortcut (and the game) keep working even after the player deletes the
	//     folder they unzipped, or downloads a newer build into a new folder.
	//     Then, ONCE, put a "Warcraft III (Online)" icon on the desktop + in the
	//     Start Menu and re-aim any existing Warcraft III shortcut at us - the
	//     icon is installed on first run only, never recreated on later launches,
	//     so deleting it sticks. WC3_NO_SHORTCUT=1 skips it entirely. Best-effort.
	if stableExe, err := ensureStableLauncher(dir); err != nil {
		fmt.Printf("(Could not set up the stable launcher copy: %v)\n", err)
	} else if os.Getenv("WC3_NO_SHORTCUT") == "" && !shortcutsInstalled() {
		if err := desktop.EnsureShortcut(stableExe, gameRoot); err != nil {
			fmt.Printf("(Could not create the shortcut: %v)\n", err)
		} else {
			fmt.Println(`A "Warcraft III (Online)" icon is on your desktop - use it to play. (Delete it if you'd rather not; it won't come back.)`)
			if n, err := desktop.RepointGameShortcuts(stableExe, gameRoot); err == nil && n > 0 {
				fmt.Printf("Pointed %d existing Warcraft III shortcut(s) here, so any of them works.\n", n)
			}
			markShortcutsInstalled()
		}
	}

	// 3. Connect. Default path is the host relay so this player can host; if the
	//    relay is unreachable (or --direct), fall back to a plain realm
	//    connection so play/joining still works.
	if !*directMode {
		err := connectViaRelay(dir, gameRoot)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errRelayUnreachable) {
			return err
		}
		fmt.Println("Host relay is unavailable right now - connecting directly. You can still play and join games.")
	}

	fmt.Printf("Configuring connection to %q...\n", gatewayName)
	if err := client.Configure(dir, serverHost, gatewayName, gatewayTimezone); err != nil {
		return fmt.Errorf("configuring gateway: %w", err)
	}
	classic := chooseClassic()
	fmt.Println("Launching Warcraft III...")
	if _, err := client.Launch(dir, gameRoot, loaderExe, classic); err != nil {
		return fmt.Errorf("launching game: %w", err)
	}
	fmt.Println("Warcraft III is starting. Have fun!")
	return nil
}

// errRelayUnreachable signals the relay could not be reached, so run() should
// fall back to a direct realm connection.
var errRelayUnreachable = errors.New("relay unreachable")

// errAlreadyRunning signals another launcher instance already owns the local
// gateway port, so this one exits with a friendly message rather than launch a
// second game that cannot connect.
var errAlreadyRunning = errors.New("launcher already running")

// connectViaRelay dials the host relay and, on success, points WC3 at the local
// gateway, launches the game, and stays resident serving the tunnel so a native
// Create Game is reachable through the server. The gateway keeps the plain
// "PvPGN" name and is written selected, so WC3 auto-uses it with nothing for
// the player to choose. Returns errRelayUnreachable (wrapped) if the relay is
// down, so the caller can fall back to direct.
func connectViaRelay(dir, gameRoot string) error {
	// Single-instance guard: only one launcher may own the local gateway port.
	// Bind it FIRST, before dialing the relay or launching WC3, so a second copy
	// exits cleanly with a message instead of starting the game and colliding.
	gateLn, err := net.Listen("tcp", hostGateAddr)
	if err != nil {
		return errAlreadyRunning
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := log.New(os.Stdout, "relay ", log.LstdFlags|log.Lmsgprefix)

	relayAddr := net.JoinHostPort(serverHost, relayPort)
	link, port, err := relaylink.Dial(ctx, relayAddr, relayToken, hostGamePort, logger, relayTLSConfig(serverHost))
	if err != nil {
		gateLn.Close()
		return fmt.Errorf("%w: %v", errRelayUnreachable, err)
	}
	fmt.Printf("Connected (relay port %d). You can Create Game and friends can join, no setup on anyone's end.\n", port)

	// pvpgn advertises a hosted game at <relay IP>:<the port WC3 declares>, so WC3
	// must host on the relay's allocated pool port P (not a fixed local port).
	// Then pvpgn advertises the relay's address:P, which is the relay's public
	// listener and is reachable from the internet, so joiners actually reach it.
	link.SetHostPort(int(port))
	if err := client.Configure(dir, hostGatewayHost, gatewayName, gatewayTimezone); err != nil {
		gateLn.Close()
		link.GameOver()
		return fmt.Errorf("configuring gateway: %w", err)
	}
	if err := client.SetGamePort(dir, int(port)); err != nil {
		gateLn.Close()
		link.GameOver()
		return fmt.Errorf("setting host game port: %w", err)
	}
	classic := chooseClassic()
	fmt.Println("Launching Warcraft III...")
	cmd, err := client.Launch(dir, gameRoot, loaderExe, classic)
	if err != nil {
		gateLn.Close()
		link.GameOver()
		return fmt.Errorf("launching game: %w", err)
	}
	// Close the launcher (releasing the relay) when Warcraft III itself exits, so
	// no stray console lingers. This watches the actual GAME process, not the W3L
	// loader or Wine desktop process - those exit right after spawning the game and
	// tearing down here would kill the relay mid-login. If the game is never
	// detected, WaitForGameExit blocks forever and the launcher stays resident.
	go func() {
		client.WaitForGameExit(cmd)
		fmt.Println("Warcraft III closed - shutting down. See you next game.")
		stop()
	}()
	fmt.Println("You are ready to host. Keep this window open while you play; it closes on its own when you quit the game.")
	if err := relaylink.ServeHost(ctx, link, gateLn, logger); err != nil && ctx.Err() == nil {
		return fmt.Errorf("host relay: %w", err)
	}
	return nil
}

// relayTLSConfig builds the launcher's TLS config for the relay tunnel. When
// relayCertPin is set (the distributed build), it pins the relay's self-signed
// certificate by its SubjectPublicKeyInfo SHA-256, which is stronger than
// public-CA trust for a server we control. Otherwise it verifies against the
// system roots.
func relayTLSConfig(host string) *tls.Config {
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS13}
	if relayCertPin != "" {
		cfg.InsecureSkipVerify = true // verified by pin below, not by CA chain
		cfg.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("relay presented no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
			if base64.StdEncoding.EncodeToString(sum[:]) != relayCertPin {
				return errors.New("relay certificate pin mismatch")
			}
			return nil
		}
	}
	return cfg
}

// chooseClassic asks whether to launch Reign of Chaos (classic) or the default
// Frozen Throne expansion, and returns true for Reign of Chaos. Enter defaults
// to Frozen Throne.
func chooseClassic() bool {
	fmt.Println()
	fmt.Println("Which game do you want to play?")
	fmt.Println("  [1] Warcraft III: The Frozen Throne  (expansion, default)")
	fmt.Println("  [2] Warcraft III: Reign of Chaos     (classic)")
	fmt.Print("Enter 1 or 2 [1]: ")
	var in string
	_, _ = fmt.Scanln(&in)
	return strings.TrimSpace(in) == "2"
}

// gameDir returns the folder that holds the game and, on Linux, its Wine prefix.
// It prefers a STABLE per-user location so re-downloading the launcher (a new
// zip in a new folder) reuses the existing install instead of reinstalling and
// re-prompting for CD keys. An install already sitting next to the binary (the
// older portable layout) is reused in place, so upgrading never forces a
// reinstall for anyone who already set one up.
func gameDir() (string, error) {
	stable, stableErr := stableGameDir()

	// 1. An install already present in the stable location wins.
	if stableErr == nil {
		if _, ok := installer.InstallPath(stable); ok {
			return stable, nil
		}
	}
	// 2. Otherwise reuse an existing portable install next to the binary
	//    (migration for anyone who installed with an earlier launcher).
	if exe, err := os.Executable(); err == nil {
		legacy := filepath.Join(filepath.Dir(exe), gameSubdir)
		if _, ok := installer.InstallPath(legacy); ok {
			return legacy, nil
		}
	}
	// 3. Fresh setup: install into the stable location so future zips reuse it.
	if stableErr == nil {
		return stable, nil
	}
	// 4. Last resort: next to the binary.
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), gameSubdir), nil
}

// ensureStableLauncher keeps a copy of this binary at a fixed path inside the
// game folder and returns it. Shortcuts point at that copy, so they keep working
// after the player deletes the folder they unzipped, and a newer download that
// replaces the copy silently upgrades every shortcut. If we are already running
// from that path (launched via the shortcut), it is returned unchanged.
func ensureStableLauncher(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(self); err == nil {
		self = abs
	}
	dest := filepath.Join(dir, launcherFileName())
	if fi, err := os.Stat(dest); err == nil {
		if sfi, err := os.Stat(self); err == nil && os.SameFile(fi, sfi) {
			return dest, nil // already running from the stable copy
		}
	}
	if err := copyExecutable(self, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func launcherFileName() string {
	if runtime.GOOS == "windows" {
		return "wc3-launcher.exe"
	}
	return "wc3-launcher"
}

// copyExecutable copies src to dst via a temp file + rename, so a shortcut never
// points at a half-written binary.
func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// stableGameDir is a per-user data location that does not move when the launcher
// binary does: %LOCALAPPDATA%\WC3 on Windows, $XDG_DATA_HOME/WC3 (or
// ~/.local/share/WC3) elsewhere.
func stableGameDir() (string, error) {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, gameSubdir), nil
		}
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(base, gameSubdir), nil
	}
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, gameSubdir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", gameSubdir), nil
}
