// Package mapsync downloads a Warcraft III server's curated map library into the
// local Maps/Download folder. It is read-only and strictly additive: it fetches
// the manifest the map daemon (cmd/wc3-mapd) serves and pulls only maps the
// player does not already have, verifying each against the manifest's SHA-256
// before it touches disk.
//
// It NEVER overwrites or deletes a local file, so a player's own custom or
// edited maps are always safe. And there is no upload path - a player never
// pushes a map to the server - because auto-distributing unvetted,
// script-carrying maps would poison every player.
package mapsync

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"wc3-launcher/internal/maplib"
)

const (
	// maxSyncMaps and maxSyncBytes bound a single sync run so a hostile or
	// compromised server cannot exhaust the player's disk by advertising a huge
	// manifest (tens of thousands of entries fit inside the manifest cap). A real
	// curated library is far below both.
	maxSyncMaps  = 4096
	maxSyncBytes = 20 << 30 // 20 GiB total written per run
)

// Reporter receives map-sync progress so a caller can draw a progress bar. A nil
// Reporter disables reporting. Its methods are called from Sync's goroutine.
type Reporter interface {
	// Plan is called once, before any download, with the number of maps and the
	// total bytes that will be fetched (maps already present locally are excluded).
	Plan(maps int, bytes int64)
	// Update is called as bytes arrive: done is completed maps, bytes is the total
	// downloaded so far (including the in-flight map), cur is its filename.
	Update(done int, bytes int64, cur string)
	// Done is called once when the sync finishes, with the number of maps written.
	Done(added int)
}

// progressReader reports each chunk read from the wrapped reader. It does not
// change what is read - the caller still caps the body and verifies its full
// SHA-256 before writing - it only observes byte counts for the progress bar.
type progressReader struct {
	r      io.Reader
	onRead func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 && p.onRead != nil {
		p.onRead(int64(n))
	}
	return n, err
}

// Sync pulls every map in the server's manifest whose name is not already
// present in mapsDir, and returns how many it wrote. It is best-effort: a
// manifest that cannot be fetched is returned as an error (the caller logs and
// carries on); a single map that fails to download or verify is logged and
// skipped, never aborting the rest.
//
// It is strictly additive. A map already present locally is left untouched, even
// if the server lists a different file under the same name - the local copy
// (which may be the player's own map) always wins. The curated library gives new
// or updated maps new, versioned names, so this never blocks getting new maps.
// tlsConf pins the server certificate when the build has a pin; nil uses defaults.
func Sync(ctx context.Context, baseURL, mapsDir string, tlsConf *tls.Config, logger *log.Logger, reporter Reporter) (int, error) {
	client := &http.Client{
		Timeout:   10 * time.Minute,
		Transport: &http.Transport{TLSClientConfig: tlsConf},
	}

	man, err := fetchManifest(ctx, client, baseURL)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(mapsDir, 0o755); err != nil {
		return 0, fmt.Errorf("creating maps folder: %w", err)
	}

	// Plan the run first so the progress bar knows the real total up front: keep
	// only maps with a safe name that we do not already have (never overwrite a
	// local file, possibly the player's own). The same aggregate budget bounds the
	// plan, so a hostile manifest cannot blow up the totals either.
	type planItem struct {
		name string
		e    maplib.Entry
	}
	var plan []planItem
	var planBytes int64
	for _, e := range man.Maps {
		if len(plan) >= maxSyncMaps || planBytes >= maxSyncBytes {
			logger.Printf("map sync budget reached (%d maps, %d bytes); stopping", len(plan), planBytes)
			break
		}
		name, ok := maplib.SafeName(e.Name)
		if !ok {
			logger.Printf("skipping map with unsafe name %q", e.Name)
			continue
		}
		if _, err := os.Stat(filepath.Join(mapsDir, name)); err == nil {
			continue // already have a map by this name - never overwrite it
		}
		plan = append(plan, planItem{name: name, e: e})
		planBytes += e.Size
	}
	if reporter != nil {
		reporter.Plan(len(plan), planBytes)
	}

	added := 0
	var written int64
	for _, it := range plan {
		if written >= maxSyncBytes {
			logger.Printf("map sync byte budget reached (%d bytes); stopping", written)
			break
		}
		dest := filepath.Join(mapsDir, it.name)
		var cur int64 // bytes of the in-flight map, for the running total
		onRead := func(n int64) {
			cur += n
			if reporter != nil {
				reporter.Update(added, written+cur, it.name)
			}
		}
		n, err := downloadOne(ctx, client, baseURL, dest, it.name, it.e, onRead)
		if err != nil {
			logger.Printf("skipping %s: %v", it.name, err)
			continue
		}
		written += n
		added++
	}
	if reporter != nil {
		reporter.Done(added)
	}
	return added, nil
}

func fetchManifest(ctx context.Context, c *http.Client, baseURL string) (maplib.Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/manifest.json", nil)
	if err != nil {
		return maplib.Manifest{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return maplib.Manifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return maplib.Manifest{}, fmt.Errorf("manifest HTTP %d", resp.StatusCode)
	}
	var m maplib.Manifest
	// Cap the manifest body so a hostile server cannot stream forever.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&m); err != nil {
		return maplib.Manifest{}, fmt.Errorf("decoding manifest: %w", err)
	}
	return m, nil
}

// downloadOne fetches one map, verifies it in full BEFORE writing (size cap,
// SHA-256 against the manifest, map header), then writes it without ever
// clobbering an existing local file. It returns the number of bytes written.
func downloadOne(ctx context.Context, c *http.Client, baseURL, dest, name string, e maplib.Entry, onRead func(int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/maps/"+url.PathEscape(name), nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// One byte over the cap so an oversized body is detected, not silently cut.
	// The progressReader only observes chunk sizes; the LimitReader still caps the
	// body and the SHA-256 below still verifies every byte before anything is kept.
	var src io.Reader = io.LimitReader(resp.Body, maplib.MaxMapSize+1)
	if onRead != nil {
		src = &progressReader{r: src, onRead: onRead}
	}
	body, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}
	if int64(len(body)) > maplib.MaxMapSize {
		return 0, fmt.Errorf("larger than %d bytes", maplib.MaxMapSize)
	}
	// The SHA-256 proves the download matches what the manifest listed; the trust
	// anchor is the pinned TLS channel that delivered the manifest, not this hash.
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != e.SHA256 {
		return 0, fmt.Errorf("sha256 mismatch")
	}
	if !maplib.HasMapHeader(body) {
		return 0, fmt.Errorf("not a Warcraft III map")
	}
	if err := writeNew(dest, body); err != nil {
		return 0, err
	}
	return int64(len(body)), nil
}

// writeNew publishes data as dest atomically and never overwrites an existing
// file. It writes a temp file, then hard-links it into place: os.Link fails if
// dest already exists, so a local map (possibly the player's own) is never
// clobbered - this closes the check-then-write race a plain Stat+Rename leaves,
// and os.Rename replaces the target on Windows. On a filesystem without
// hardlinks (e.g. FAT), it falls back to a Stat-guarded rename.
func writeNew(dest string, data []byte) error {
	tmp := dest + ".part"
	_ = os.Remove(tmp) // clear any leftover from an interrupted run
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	err := os.Link(tmp, dest)
	if err == nil {
		_ = os.Remove(tmp)
		return nil
	}
	if errors.Is(err, os.ErrExist) {
		_ = os.Remove(tmp)
		return nil // a local map is there - keep it, we did not overwrite
	}
	// Link unsupported on this volume: fall back to a rename, still re-checking
	// dest so we avoid clobbering a local map (racy, but not remotely triggerable).
	if _, statErr := os.Stat(dest); statErr == nil {
		_ = os.Remove(tmp)
		return nil
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
