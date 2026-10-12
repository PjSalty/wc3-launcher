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

// The cache was keyed on the directory's mtime, which does not move when a file
// is rewritten in place: the manifest kept the old hash while the new bytes were
// served, so every launcher rejected the download.
func TestManifestCacheSeesAnInPlaceRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.w3x")
	if err := os.WriteFile(path, []byte("HM3Wversion-1"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &manifestCache{dir: dir}
	before, err := c.get()
	if err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("HM3Wversion-2"), 0o644); err != nil { // same length
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	// Put the directory's mtime back, as an in-place rewrite leaves it.
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}

	after, err := c.get()
	if err != nil {
		t.Fatal(err)
	}
	if string(after) == string(before) {
		t.Fatalf("manifest unchanged after the map was rewritten in place: %s", after)
	}
}

func TestHandlerServesRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("HM3Wnot-in-the-library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.w3x")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	srv := httptest.NewServer(handler(dir))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/maps/link.w3x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a symlink out of the library was served with status %d, want 404", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL + "/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	if strings.Contains(string(buf[:n]), "link.w3x") {
		t.Errorf("the manifest lists a symlink: %s", buf[:n])
	}
}

func TestTLSMode(t *testing.T) {
	cases := []struct {
		cert, key string
		insecure  bool
		useTLS    bool
		wantErr   bool
	}{
		{"c.pem", "k.pem", false, true, false},
		{"", "", true, false, false},      // plain HTTP, asked for by name
		{"", "", false, false, true},      // nothing given: refuse, do not fall back
		{"c.pem", "", false, false, true}, // half a certificate
		{"", "k.pem", false, false, true},
		{"c.pem", "k.pem", true, true, false}, // a full certificate wins
		{"c.pem", "", true, false, true},
	}
	for _, c := range cases {
		got, err := tlsMode(c.cert, c.key, c.insecure)
		if got != c.useTLS || (err != nil) != c.wantErr {
			t.Errorf("tlsMode(%q, %q, %v) = %v, %v; want %v, err=%v", c.cert, c.key, c.insecure, got, err, c.useTLS, c.wantErr)
		}
	}
}
