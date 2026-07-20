// Package maplib defines the shared shape of a Warcraft III server's curated map
// library: the manifest the map daemon (cmd/wc3-mapd) serves and the launcher
// (internal/mapsync) reads to sync maps read-only.
//
// The library is operator-curated. There is deliberately no upload path anywhere
// in this code: a .w3x carries executable map script, so auto-accepting player
// uploads would let anyone poison every player's map folder. The only writer is
// the operator, dropping vetted files into the served directory.
package maplib

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry describes one map in the library.
type Entry struct {
	Name   string `json:"name"`   // bare filename, e.g. "DotA_v6.83d.w3x"
	SHA256 string `json:"sha256"` // lowercase hex of the file contents
	Size   int64  `json:"size"`   // bytes
}

// Manifest is the whole library listing the daemon serves at /manifest.json.
type Manifest struct {
	Maps []Entry `json:"maps"`
}

// MaxMapSize caps a single map. WC3 maps are a few MB; 128 MiB is far above any
// real classic map and bounds memory when a download is verified in full before
// it touches disk, so a hostile or broken server cannot make the launcher buffer
// an unbounded body.
const MaxMapSize = 128 << 20

// IsMapFile reports whether name has a Warcraft III map extension (.w3x for
// expansion maps, .w3m for classic). Case-insensitive.
func IsMapFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".w3x", ".w3m":
		return true
	}
	return false
}

// SafeName returns the bare filename if name is a plain map filename safe to
// read from or write into a single directory, and false otherwise. It rejects
// any path separator, any "..", a colon (drive-relative paths and NTFS alternate
// data streams like "map.w3x:evil"), control characters, non-base paths, any
// non-map extension, and Windows reserved device names. Both the daemon
// (serving) and the launcher (writing) run every name through this, so a hostile
// manifest can neither escape the maps directory, attach a stream to an existing
// file, nor drop a non-map file - on Linux or on the Windows launcher.
func SafeName(name string) (string, bool) {
	if name == "" || strings.ContainsAny(name, `/\:`) || strings.Contains(name, "..") {
		return "", false
	}
	for _, r := range name {
		if r < 0x20 { // control characters, including NUL
			return "", false
		}
	}
	if name != filepath.Base(name) {
		return "", false
	}
	if !IsMapFile(name) {
		return "", false
	}
	// Reject Windows reserved device names (CON, NUL, COM1, ...): reserved even
	// with an extension (NUL.w3x still names the null device), and the launcher
	// also runs on Windows.
	stem := name
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return "", false
	}
	return name, true
}

// HasMapHeader reports whether b begins with a Warcraft III map magic: "HM3W"
// (the war3map header stock maps carry) or "MPQ\x1a" (a raw MPQ archive, which
// some maps are). This is a structural sanity check, NOT a security control:
// against a hostile server the manifest's SHA-256 is self-referential (the
// server picks both the bytes and the hash), so content trust rests on the
// pinned TLS channel to your own server plus the operator curating the library.
// The header check only cheaply rejects an obviously-non-map file before it is
// written into Maps/Download.
func HasMapHeader(b []byte) bool {
	return bytes.HasPrefix(b, []byte("HM3W")) || bytes.HasPrefix(b, []byte("MPQ\x1a"))
}

// Sum returns the lowercase-hex SHA-256 and byte size of the file at path.
func Sum(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// ScanDir builds a Manifest from every Warcraft III map file directly inside dir
// (non-recursive), sorted by name for a stable listing. A dir that does not
// exist yet yields an empty manifest, not an error, so the daemon starts fine
// before the operator has added any maps.
func ScanDir(dir string) (Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return Manifest{Maps: []Entry{}}, nil
		}
		return Manifest{}, err
	}
	m := Manifest{Maps: []Entry{}}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, ok := SafeName(e.Name())
		if !ok {
			continue
		}
		sum, size, err := Sum(filepath.Join(dir, name))
		if err != nil {
			continue // skip a file we cannot read rather than failing the whole listing
		}
		m.Maps = append(m.Maps, Entry{Name: name, SHA256: sum, Size: size})
	}
	sort.Slice(m.Maps, func(i, j int) bool { return m.Maps[i].Name < m.Maps[j].Name })
	return m, nil
}
