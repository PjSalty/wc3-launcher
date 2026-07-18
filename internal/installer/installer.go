// Package installer fetches and runs Blizzard's official free legacy installer
// for Warcraft III: The Frozen Throne. The client comes from Blizzard's own
// download endpoint; this launcher never redistributes the game itself.
package installer

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// installerName is the file the Blizzard downloader is saved as.
const installerName = "Warcraft3_TFT_Setup.exe"

// downloadTimeout bounds the whole installer fetch (it is a small downloader
// stub, not the multi-GB game data).
const downloadTimeout = 10 * time.Minute

// httpsOnlyClient fetches the installer over HTTPS and refuses to be redirected
// onto plaintext. Blizzard's endpoint redirects to its CDN, and this executable
// is run on the player's machine afterwards, so an http:// hop anywhere in the
// chain would let a network attacker swap the binary.
func httpsOnlyClient() *http.Client {
	return &http.Client{
		Timeout: downloadTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing insecure redirect to %s (https required)", req.URL.Scheme+"://"+req.URL.Host)
			}
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
}

// HasExpansion reports whether the Frozen Throne expansion data (War3x.mpq) is
// present in gameRoot. Without it the game can only run Reign of Chaos, so the
// launcher treats a base-only install as needing the expansion step.
func HasExpansion(gameRoot string) bool {
	_, err := os.Stat(filepath.Join(gameRoot, "War3x.mpq"))
	return err == nil
}

// Download fetches Blizzard's official legacy TFT downloader into dir and
// returns the saved path. It follows Blizzard's redirect to downloader.battle.net.
func Download(dir, rawURL string) (string, error) {
	// The downloaded file is executed on the player's machine, so require HTTPS
	// end to end (start URL and every redirect hop).
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("bad installer URL: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("refusing to fetch the installer over %q: https required", u.Scheme)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", dir, err)
	}
	dst := filepath.Join(dir, installerName)

	fmt.Println("Fetching the official Warcraft III installer from Blizzard...")
	resp, err := httpsOnlyClient().Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("downloading Blizzard installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading Blizzard installer: server returned %s", resp.Status)
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("saving installer: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("saving installer: %w", err)
	}
	return dst, nil
}
