// Command bundle-builder packages the small loader-and-maps bundle that the
// launcher downloads from the portal. It does NOT include Warcraft III itself:
// the game client is installed from Blizzard's official download by the
// launcher. This bundle carries only the open-source W3L loader and the maps.
//
// Usage:
//
//	bundle-builder -loader <dir> -maps <dir> -out wc3-bundle.zip
//
// -loader must contain w3l.exe and w3lh.dll (from github.com/pvpgn/w3l).
// -maps is optional; its contents land under Maps/Download in the game folder.
package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	loaderDir := flag.String("loader", "", "directory containing w3l.exe and w3lh.dll (required)")
	mapsDir := flag.String("maps", "", "directory of maps to place under Maps/Download (optional)")
	out := flag.String("out", "wc3-bundle.zip", "output zip path")
	flag.Parse()

	if err := run(*loaderDir, *mapsDir, *out); err != nil {
		fmt.Fprintf(os.Stderr, "bundle-builder: %v\n", err)
		os.Exit(1)
	}
}

func run(loaderDir, mapsDir, out string) error {
	if loaderDir == "" {
		return fmt.Errorf("-loader is required (the folder with w3l.exe and w3lh.dll)")
	}
	for _, f := range []string{"w3l.exe", "w3lh.dll"} {
		if _, err := os.Stat(filepath.Join(loaderDir, f)); err != nil {
			return fmt.Errorf("loader is missing %s: %w", f, err)
		}
	}

	zf, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("creating %s: %w", out, err)
	}
	defer zf.Close()
	zw := zip.NewWriter(zf)
	defer zw.Close()

	// Loader files go at the root of the game folder.
	if err := addTree(zw, loaderDir, ""); err != nil {
		return fmt.Errorf("adding loader: %w", err)
	}
	// Maps go under Maps/Download.
	if mapsDir != "" {
		if err := addTree(zw, mapsDir, "Maps/Download"); err != nil {
			return fmt.Errorf("adding maps: %w", err)
		}
	}

	fmt.Printf("wrote %s\n", out)
	return nil
}

// addTree walks src and writes every file into the zip under prefix.
func addTree(zw *zip.Writer, src, prefix string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Join(prefix, rel))
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(w, in)
		return err
	})
}
