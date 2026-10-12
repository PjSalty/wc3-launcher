package mapsync

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"wc3-launcher/internal/maplib"
)

// tlsFor returns a client config that trusts srv's test certificate, standing in
// for the pinned config the launcher passes.
func tlsFor(srv *httptest.Server) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{RootCAs: pool}
}

func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

// library serves man and, for every listed name, body. hits counts requests.
func library(man maplib.Manifest, body []byte, hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.URL.Path == "/manifest.json" {
			_ = json.NewEncoder(w).Encode(man)
			return
		}
		_, _ = w.Write(body)
	})
}

func entryFor(name string, body []byte) maplib.Entry {
	sum := sha256.Sum256(body)
	return maplib.Entry{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}
}

func TestSyncAdditiveAndSafe(t *testing.T) {
	good := []byte("HM3Wreal-map-bytes")
	sum := sha256.Sum256(good)
	goodSHA := hex.EncodeToString(sum[:])

	man := maplib.Manifest{Maps: []maplib.Entry{
		{Name: "good.w3x", SHA256: goodSHA, Size: int64(len(good))},
		{Name: "mine.w3x", SHA256: goodSHA, Size: int64(len(good))}, // player already has this name
		{Name: "bad.w3x", SHA256: goodSHA, Size: 5},                 // body will not match this hash
		{Name: "../evil.w3x", SHA256: goodSHA, Size: 1},             // traversal name
	}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.json":
			_ = json.NewEncoder(w).Encode(man)
		case "/maps/good.w3x", "/maps/mine.w3x":
			_, _ = w.Write(good)
		case "/maps/bad.w3x":
			_, _ = w.Write([]byte("WRONG")) // wrong hash and no map header
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	// The player's own custom map, under a name the server also lists.
	local := []byte("MY OWN CUSTOM MAP - do not touch")
	if err := os.WriteFile(filepath.Join(dir, "mine.w3x"), local, 0o644); err != nil {
		t.Fatal(err)
	}

	logger := log.New(io.Discard, "", 0)
	rep := &captureReporter{}
	n, err := Sync(context.Background(), srv.URL, dir, tlsFor(srv), logger, rep)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 1 {
		t.Fatalf("added %d, want 1 (good only; mine skipped, bad rejected, evil unsafe)", n)
	}
	// The reporter's plan is the maps it will ATTEMPT: good + bad (mine is already
	// present, evil has an unsafe name, both filtered before download). bad then
	// fails hash verification at download time, so Done reports 1 written. The bar
	// getting byte updates proves the streaming progress path is wired.
	if rep.planMaps != 2 {
		t.Errorf("reporter Plan maps = %d, want 2 (good + bad attempted)", rep.planMaps)
	}
	if rep.updates == 0 {
		t.Error("reporter got no Update calls during the download")
	}
	if rep.done != 1 {
		t.Errorf("reporter Done added = %d, want 1", rep.done)
	}

	// The local custom map must be byte-for-byte untouched.
	if got, _ := os.ReadFile(filepath.Join(dir, "mine.w3x")); string(got) != string(local) {
		t.Error("sync OVERWROTE a local custom map")
	}
	// good.w3x written correctly.
	if got, _ := os.ReadFile(filepath.Join(dir, "good.w3x")); string(got) != string(good) {
		t.Error("good.w3x content wrong")
	}
	// bad.w3x rejected on hash mismatch: never written.
	if _, err := os.Stat(filepath.Join(dir, "bad.w3x")); !os.IsNotExist(err) {
		t.Error("bad.w3x (hash mismatch) should not exist")
	}
	// traversal never escaped the maps dir.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.w3x")); !os.IsNotExist(err) {
		t.Error("traversal escaped the maps dir")
	}

	// Re-sync is a no-op: everything present is left alone.
	if n2, err := Sync(context.Background(), srv.URL, dir, tlsFor(srv), logger, nil); err != nil || n2 != 0 {
		t.Errorf("re-sync added %d (err %v), want 0", n2, err)
	}
}

// captureReporter records the progress callbacks so a test can assert the bar is
// driven correctly. A nil *captureReporter is not used; Sync takes nil directly.
type captureReporter struct {
	planMaps  int
	planBytes int64
	updates   int
	done      int
}

func (c *captureReporter) Plan(maps int, bytes int64)             { c.planMaps, c.planBytes = maps, bytes }
func (c *captureReporter) Update(done int, bytes int64, s string) { c.updates++ }
func (c *captureReporter) Done(added int)                         { c.done = added }

func TestSyncManifestError(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := Sync(context.Background(), srv.URL, t.TempDir(), tlsFor(srv), quiet(), nil); err == nil {
		t.Error("expected an error when the manifest fetch fails")
	}
}

// The manifest is trusted for the channel it arrives on. A plain http base URL
// (WC3_MAPS_URL accepted one) has no such channel.
func TestSyncRefusesAnythingButHTTPS(t *testing.T) {
	body := []byte("HM3Wmap")
	var hits atomic.Int32
	srv := httptest.NewServer(library(maplib.Manifest{Maps: []maplib.Entry{entryFor("a.w3x", body)}}, body, &hits))
	defer srv.Close()

	dir := t.TempDir()
	if _, err := Sync(context.Background(), srv.URL, dir, nil, quiet(), nil); err == nil {
		t.Fatal("Sync accepted a plain http map server")
	}
	if hits.Load() != 0 {
		t.Fatalf("Sync sent %d request(s) to a plain http map server", hits.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "a.w3x")); !os.IsNotExist(err) {
		t.Fatal("a map from a plain http server was written")
	}
}

// A redirect used to be followed off the pinned channel: to plain http, to
// another host, or to a service on the player's own machine.
func TestSyncDoesNotFollowRedirects(t *testing.T) {
	body := []byte("HM3Wmap")
	var elsewhereHits atomic.Int32
	elsewhere := httptest.NewServer(library(maplib.Manifest{Maps: []maplib.Entry{entryFor("a.w3x", body)}}, body, &elsewhereHits))
	defer elsewhere.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, err := Sync(context.Background(), srv.URL, dir, tlsFor(srv), quiet(), nil); err == nil {
		t.Fatal("Sync succeeded through a redirect")
	}
	if elsewhereHits.Load() != 0 {
		t.Fatalf("Sync followed the redirect: %d request(s) reached the other server", elsewhereHits.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "a.w3x")); !os.IsNotExist(err) {
		t.Fatal("a map fetched through a redirect was written")
	}
}

// WC3 and Windows do not tell DotA.w3x from dota.w3x. On a case-sensitive disk
// the second spelling was written next to the player's own map.
func TestSyncTreatsNamesWithoutCase(t *testing.T) {
	body := []byte("HM3Wserver-copy")
	srv := httptest.NewTLSServer(library(maplib.Manifest{Maps: []maplib.Entry{
		entryFor("dota.w3x", body), // the player has DotA.w3x
		entryFor("New.w3x", body),
		entryFor("NEW.W3X", body), // the same name again, later in the manifest
	}}, body, nil))
	defer srv.Close()

	dir := t.TempDir()
	mine := []byte("HM3Wmy-own-edit")
	if err := os.WriteFile(filepath.Join(dir, "DotA.w3x"), mine, 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := Sync(context.Background(), srv.URL, dir, tlsFor(srv), quiet(), nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if n != 1 || len(names) != 2 {
		t.Fatalf("added %d, folder now holds %v; want 1 added and exactly DotA.w3x plus New.w3x", n, names)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "DotA.w3x")); string(got) != string(mine) {
		t.Fatal("the player's own map was replaced")
	}
}

func TestSyncHoldsEntriesToTheirManifestValues(t *testing.T) {
	body := []byte("HM3Wmap-bytes")
	good := entryFor("good.w3x", body)
	short := entryFor("short.w3x", body)
	short.Size-- // the hash matches, the size does not
	zero := entryFor("zero.w3x", body)
	zero.Size = 0
	negative := entryFor("negative.w3x", body)
	negative.Size = -1 << 40 // would pull the byte budget below zero
	huge := entryFor("huge.w3x", body)
	huge.Size = maplib.MaxMapSize + 1
	nohash := entryFor("nohash.w3x", body)
	nohash.SHA256 = "not-a-hash"

	srv := httptest.NewTLSServer(library(maplib.Manifest{Maps: []maplib.Entry{short, zero, negative, huge, nohash, good}}, body, nil))
	defer srv.Close()

	dir := t.TempDir()
	rep := &captureReporter{}
	n, err := Sync(context.Background(), srv.URL, dir, tlsFor(srv), quiet(), rep)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("added %d, want only good.w3x", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "good.w3x")); err != nil {
		t.Fatalf("good.w3x missing: %v", err)
	}
	// short.w3x is well-formed, so it is planned and then fails on download.
	if want := good.Size + short.Size; rep.planMaps != 2 || rep.planBytes != want {
		t.Fatalf("plan = %d maps / %d bytes, want 2 maps / %d bytes", rep.planMaps, rep.planBytes, want)
	}
}
