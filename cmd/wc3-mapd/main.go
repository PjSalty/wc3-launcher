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
	"encoding/json"
	"flag"
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
	cert := flag.String("cert", "", "TLS certificate file (enables HTTPS; recommended)")
	key := flag.String("key", "", "TLS key file")
	flag.Parse()

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
	var err error
	if *cert != "" && *key != "" {
		err = srv.ListenAndServeTLS(*cert, *key)
	} else {
		log.Print("warning: serving plain HTTP; pass -cert and -key to enable TLS")
		err = srv.ListenAndServe()
	}
	if err != nil {
		log.Fatalf("wc3-mapd: %v", err)
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
		http.ServeFile(w, r, filepath.Join(dir, name))
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
// map) only when the maps directory changes. Hashing a large library on every
// request would pin CPU; a directory's mtime changes whenever a map is added or
// removed, which for a curated library is the only time the manifest can change
// (new or updated maps arrive under new names), so an mtime check is a cheap and
// correct invalidation. The first request after a change does the scan under the
// lock; concurrent requests wait and then serve the fresh cache.
type manifestCache struct {
	dir string
	mu  sync.Mutex
	mod time.Time
	buf []byte
}

func (c *manifestCache) get() ([]byte, error) {
	var mod time.Time
	if fi, err := os.Stat(c.dir); err == nil {
		mod = fi.ModTime()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf != nil && mod.Equal(c.mod) {
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
	c.buf, c.mod = b, mod
	return b, nil
}
