package mapsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"wc3-launcher/internal/maplib"
)

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	n, err := Sync(context.Background(), srv.URL, dir, nil, logger)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if n != 1 {
		t.Fatalf("added %d, want 1 (good only; mine skipped, bad rejected, evil unsafe)", n)
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
	if n2, err := Sync(context.Background(), srv.URL, dir, nil, logger); err != nil || n2 != 0 {
		t.Errorf("re-sync added %d (err %v), want 0", n2, err)
	}
}

func TestSyncManifestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := Sync(context.Background(), srv.URL, t.TempDir(), nil, log.New(io.Discard, "", 0)); err == nil {
		t.Error("expected an error when the manifest fetch fails")
	}
}
