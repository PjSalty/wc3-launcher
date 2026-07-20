package maplib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeName(t *testing.T) {
	good := []string{"DotA_v6.83d.w3x", "Legion TD.w3x", "footmen.w3m", "Green.W3X"}
	for _, n := range good {
		if _, ok := SafeName(n); !ok {
			t.Errorf("SafeName(%q) = false, want true", n)
		}
	}
	bad := []string{
		"", "../etc/passwd.w3x", "a/b.w3x", `a\b.w3x`, "map.txt", "/abs.w3x", "map..w3x", "sub/../x.w3x",
		"a.w3x:evil.w3x", "C:foo.w3x", // colon: NTFS stream / drive-relative
		"NUL.w3x", "con.w3x", "COM1.w3x", "LPT9.w3x", // Windows reserved device names
		"a\x00.w3x", "tab\t.w3x", // control characters
	}
	for _, n := range bad {
		if _, ok := SafeName(n); ok {
			t.Errorf("SafeName(%q) = true, want false", n)
		}
	}
}

func TestHasMapHeader(t *testing.T) {
	if !HasMapHeader([]byte("HM3W\x00\x00")) {
		t.Error("HM3W header rejected")
	}
	if !HasMapHeader([]byte("MPQ\x1a\x00")) {
		t.Error("MPQ header rejected")
	}
	if HasMapHeader([]byte("PK\x03\x04")) {
		t.Error("zip accepted as a map")
	}
	if HasMapHeader([]byte("ab")) {
		t.Error("short junk accepted")
	}
}

func TestScanDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.w3x", "HM3Wmap-a")
	write(t, dir, "b.w3m", "MPQ\x1amap-b")
	write(t, dir, "notes.txt", "ignore me") // not a map: skipped
	m, err := ScanDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Maps) != 2 {
		t.Fatalf("scanned %d maps, want 2 (txt ignored)", len(m.Maps))
	}
	if m.Maps[0].Name != "a.w3x" || m.Maps[1].Name != "b.w3m" {
		t.Errorf("unsorted or wrong: %+v", m.Maps)
	}
	if m.Maps[0].Size != 9 || m.Maps[0].SHA256 == "" {
		t.Errorf("bad entry: %+v", m.Maps[0])
	}
	if mm, err := ScanDir(filepath.Join(dir, "does-not-exist")); err != nil || len(mm.Maps) != 0 {
		t.Errorf("ScanDir(missing) = %+v, %v; want empty, nil", mm, err)
	}
}

func write(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
