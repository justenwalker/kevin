// Package pkgcache locates the shared, content-addressed cache of plugin
// package blobs fetched by the oci: and http: plugin sources - the same
// sha256-addressed bytes dedupe into one entry regardless of which source
// fetched them.
package pkgcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/state"
)

// Dir is the pkg-cache directory under the user state directory - global, shared across every project.
func Dir() string { return state.ForUser().PackageCache() }

// Sha256Dir is the subdirectory every sha256-addressed cache entry lands
// in - the parent directory of every Path result. A caller that stages a
// file for an eventual atomic rename into the cache, before it knows the
// digest that names the final entry, creates it here.
func Sha256Dir() string {
	return filepath.Join(Dir(), "sha256")
}

// Path is the local path a blob with the given sha256 hex digest is cached
// at.
func Path(hexDigest string) string {
	return filepath.Join(Sha256Dir(), hexDigest)
}

// Entry is a cached package blob.
type Entry struct {
	// Path is where the blob is on disk.
	Path string

	// Digest is the blob's content digest, "sha256:<hex>".
	Digest string
}

// Store streams r into the cache, atomically, under its sha256 digest. When
// wantHex is set, content that hashes to anything else is rejected with
// [ErrDigestMismatch] before it reaches the cache.
func Store(r io.Reader, wantHex string) (Entry, error) {
	dir := Sha256Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Entry{}, fmt.Errorf("pkgcache: create %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "*.tmp") // same filesystem as the cache, for an atomic rename
	if err != nil {
		return Entry{}, fmt.Errorf("pkgcache: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once renamed away

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close() //nolint:errcheck,gosec // best effort; the copy error is what's reported
		return Entry{}, fmt.Errorf("pkgcache: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Entry{}, fmt.Errorf("pkgcache: close temp file: %w", err)
	}

	gotHex := hex.EncodeToString(h.Sum(nil))
	if wantHex != "" && gotHex != wantHex {
		return Entry{}, fmt.Errorf("pkgcache: got sha256:%s: %w", gotHex, ErrDigestMismatch)
	}
	path := Path(gotHex)
	if err := os.Rename(tmp.Name(), path); err != nil {
		return Entry{}, fmt.Errorf("pkgcache: %w", err)
	}
	return Entry{Path: path, Digest: "sha256:" + gotHex}, nil
}
