package bundle

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestUnzipRejectsZipSlip proves the extractor refuses an entry that would
// escape the destination directory (the one security-critical branch here).
func TestUnzipRejectsZipSlip(t *testing.T) {
	dir := t.TempDir()
	data := makeZip(t, "../escape.txt", "pwned")

	dest := filepath.Join(dir, "out")
	if err := unzip(data, dest); err == nil {
		t.Fatal("expected zip-slip path to be rejected, got nil error")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("zip-slip escaped the destination directory")
	}
}

// TestUnzipExtractsNested proves a normal nested entry lands in the right place.
func TestUnzipExtractsNested(t *testing.T) {
	dir := t.TempDir()
	data := makeZip(t, "Maps/Download/footmen.w3x", "map")

	dest := filepath.Join(dir, "out")
	if err := unzip(data, dest); err != nil {
		t.Fatalf("unzip: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "Maps", "Download", "footmen.w3x"))
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != "map" {
		t.Fatalf("content = %q, want %q", got, "map")
	}
}

// TestEmbeddedAssetIsValidZip proves the embedded loader archive is present and
// readable (catches a missing/corrupt assets/wc3-bundle.zip at test time).
func TestEmbeddedAssetIsValidZip(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "out")
	if err := unzip(asset, dest); err != nil {
		t.Fatalf("embedded bundle failed to extract: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "w3l.exe")); err != nil {
		t.Fatalf("embedded bundle missing w3l.exe loader: %v", err)
	}
}

func makeZip(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
