// Package pkgcache locates the shared, content-addressed cache of plugin
// package blobs fetched by the oci: and http: plugin sources - the same
// sha256-addressed bytes dedupe into one entry regardless of which source
// fetched them.
package pkgcache

import (
	"os"
	"path/filepath"
)

// Dir is ~/.kevin/pkg-cache - global, shared across every project.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kevin", "pkg-cache")
	}
	return filepath.Join(home, ".kevin", "pkg-cache")
}

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
