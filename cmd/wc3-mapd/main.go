// Command wc3-mapd serves a Warcraft III server's curated map library over
// read-only HTTP(S): a manifest of the maps and the map files themselves. The
// launcher (internal/mapsync) syncs from it so every player has the same maps
// and nobody waits on an in-game map transfer.
//
// It is deliberately read-only. There is no upload endpoint: the library is
// curated by the operator (drop vetted .w3x/.w3m files into the served
// directory), never by clients. A .w3x carries executable map script, and
// auto-accepting player uploads would let anyone poison every player's map
// folder, so that path does not exist.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"wc3-launcher/internal/maplib"
)

func main() {
	dir := flag.String("dir", "/maps", "directory of curated .w3x/.w3m maps to serve")
	addr := flag.String("addr", ":7443", "listen address")
	cert := flag.String("cert", "", "TLS certificate file")
	key := flag.String("key", "", "TLS key file")
	insecure := flag.Bool("insecure-http", false, "serve plain HTTP with no certificate; launchers only sync over HTTPS, so this is for a local test")
	flag.Parse()

	useTLS, err := tlsMode(*cert, *key, *insecure)
	if err != nil {
		log.Fatalf("wc3-mapd: %v", err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler(*dir),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second, // requests are tiny GETs
		// WriteTimeout accommodates a large map streamed over a slow link, while
		// still cutting a client that stalls mid-transfer (slowloris).
		WriteTimeout: 10 * time.Minute,
		IdleTimeout:  60 * time.Second,
	}
	log.Printf("wc3-mapd serving %s on %s (read-only)", *dir, *addr)
	if useTLS {
		err = srv.ListenAndServeTLS(*cert, *key)
	} else {
		log.Print("warning: serving plain HTTP (-insecure-http); launchers will not sync from this")
		err = srv.ListenAndServe()
	}
	if err != nil {
		log.Fatalf("wc3-mapd: %v", err)
	}
}

// tlsMode decides how to serve. HTTPS needs both files. Plain HTTP has to be
// asked for by name: the launcher trusts the manifest because of the pinned TLS
// channel it arrives on, so a daemon that quietly fell back to HTTP when a
// certificate path was missing would look up and serve nobody.
func tlsMode(cert, key string, insecure bool) (useTLS bool, err error) {
	switch {
	case cert != "" && key != "":
		return true, nil
	case insecure && cert == "" && key == "":
		return false, nil
	case insecure:
		return false, errors.New("-insecure-http was given together with a certificate or key; pick one")
	default:
		return false, errors.New("no TLS certificate: pass -cert and -key, or -insecure-http to serve plain HTTP on purpose")
	}
}

// handler routes the two read-only endpoints. Every other path, and every method
// other than GET/HEAD, is refused: the daemon can only be read.
func handler(dir string) http.Handler {
	mux := http.NewServeMux()
	cache := &manifestCache{dir: dir}

	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		if !readOnly(w, r) {
			return
		}
		b, err := cache.get()
		if err != nil {
			http.Error(w, "cannot read library", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	})

	mux.HandleFunc("/maps/", func(w http.ResponseWriter, r *http.Request) {
		if !readOnly(w, r) {
			return
		}
		// SafeName rejects any traversal or non-map name, so only a bare
		// .w3x/.w3m from inside dir can ever be served.
		name, ok := maplib.SafeName(strings.TrimPrefix(r.URL.Path, "/maps/"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		// Regular files only, the same rule the manifest scan applies: a symlink
		// in the library would be served through to whatever it points at.
		path := filepath.Join(dir, name)
		if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	})

	return mux
}

// readOnly rejects any method other than GET/HEAD so the daemon can never be
// written to. It returns false (after writing the response) when it rejects.
func readOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "read-only", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// manifestCache serves the library manifest, recomputing it (which hashes every
// map) only when the library changes. Hashing a large library on every request
// would pin CPU, so each request only stats the files: the cache key is every
// map's name, size and mtime. The directory's own mtime is not enough, because
// it does not move when a file is rewritten in place, and the manifest would
// then keep the old hash while the new bytes were served. The first request
// after a change does the scan under the lock; concurrent requests wait and
// then serve the fresh cache.
type manifestCache struct {
	dir string
	mu  sync.Mutex
	key string
	buf []byte
}

func (c *manifestCache) get() ([]byte, error) {
	key, err := libraryKey(c.dir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf != nil && key == c.key {
		return c.buf, nil
	}
	m, err := maplib.ScanDir(c.dir)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	c.buf, c.key = b, key
	return b, nil
}

// libraryKey fingerprints the library from directory metadata alone. A missing
// directory is an empty library, as it is for ScanDir.
func libraryKey(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "empty", nil
		}
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue // gone between the listing and the stat
		}
		fmt.Fprintf(h, "%q %d %d\n", fi.Name(), fi.Size(), fi.ModTime().UnixNano())
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
