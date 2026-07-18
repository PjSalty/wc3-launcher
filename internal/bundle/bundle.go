// Package bundle carries the preconfigured Warcraft III overlay (the W3L loader
// w3l.exe + w3lh.dll) embedded in the launcher binary, so the launcher is a
// single self-contained download with no portal or network dependency for the
// loader. The game client itself is installed separately from Blizzard's CDN.
package bundle

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// asset is the loader overlay, zipped. It is embedded per-OS (see
// bundle_windows.go / bundle_linux.go): Windows carries only the W3L loader
// plus d3d8to9 (WC3 renders natively there), while Linux also carries the DXVK
// d3d9.dll needed for reliable rendering under Wine. That keeps the Windows
// download small.

// Ensure extracts the embedded bundle (identified by ver) into dir. It re-runs
// only when the installed version marker differs from ver, so repeat launches
// skip extraction entirely.
func Ensure(dir, ver string) error {
	marker := filepath.Join(dir, ".bundle-version")
	if installed, _ := os.ReadFile(marker); strings.TrimSpace(string(installed)) == ver {
		return nil // already current
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	fmt.Println("Installing game files...")
	if err := unzip(asset, dir); err != nil {
		return err
	}

	if err := os.WriteFile(marker, []byte(ver), 0o644); err != nil {
		return fmt.Errorf("writing version marker: %w", err)
	}
	return nil
}

func unzip(data []byte, dest string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("opening embedded archive: %w", err)
	}

	cleanDest := filepath.Clean(dest)
	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)
		// Guard against zip-slip: a crafted entry name must not escape dest.
		if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in archive: %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := extractFile(f, target); err != nil {
			return fmt.Errorf("extracting %s: %w", f.Name, err)
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()

	// shortcut: unbounded copy. The bundle is our own embedded artifact; add an
	// io.LimitReader cap if it ever comes from an untrusted source.
	if _, err := io.Copy(out, rc); err != nil {
		return err
	}
	return nil
}
