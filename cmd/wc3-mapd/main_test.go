package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestCacheInvalidates(t *testing.T) {
	dir := t.TempDir()
	c := &manifestCache{dir: dir}

	// Empty library: manifest has no maps.
	b1, err := c.get()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b1), "a.w3x") {
		t.Fatalf("empty manifest already had a map: %s", b1)
	}

	// Add a map: the dir mtime changes, so the next get() rescans and picks it up
	// rather than serving the stale empty cache.
	if err := os.WriteFile(filepath.Join(dir, "a.w3x"), []byte("HM3Wmap"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A real add changes the dir mtime, but a fast test on a coarse-granularity
	// filesystem (some CI runners) can add the file within the same mtime tick as
	// the first scan. Force the mtime forward so this exercises the invalidation
	// logic, not the filesystem's clock resolution.
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	bump := fi.ModTime().Add(time.Hour)
	if err := os.Chtimes(dir, bump, bump); err != nil {
		t.Fatal(err)
	}
	b2, err := c.get()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), "a.w3x") {
		t.Errorf("cache did not invalidate after a map was added: %s", b2)
	}
}

func TestHandler(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.w3x"), []byte("HM3Wmap"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler(dir))
	defer srv.Close()

	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/manifest.json"); code != http.StatusOK {
		t.Errorf("manifest status %d, want 200", code)
	}
	if code := get("/maps/a.w3x"); code != http.StatusOK {
		t.Errorf("map status %d, want 200", code)
	}
	if code := get("/maps/secret.txt"); code == http.StatusOK {
		t.Error("served a non-map file")
	}
	if code := get("/maps/..%2f..%2fetc%2fpasswd"); code == http.StatusOK {
		t.Error("path traversal returned 200")
	}

	// Writes are refused.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/maps/a.w3x", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status %d, want 405", resp.StatusCode)
	}
}
