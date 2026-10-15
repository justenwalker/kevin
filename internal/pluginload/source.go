package pluginload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pkgcache"
	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// Detached signature file suffixes: minisign's own default for
// `minisign -S`/`-Sm`, and `cosign sign-blob --bundle`'s default naming.
const (
	minisigSuffix  = ".minisig"
	sigstoreSuffix = ".sigstore.json"
)

// source is where a plugin package comes from.
type source interface {
	// fetch makes the package available on local disk. The returned Digest is
	// the checksum pluginpkg.Extract verifies the package against.
	fetch(ctx context.Context) (pkgcache.Entry, error)
	// signature returns the detached signature for scheme that ships with
	// the package f.
	signature(ctx context.Context, f pkgcache.Entry, scheme config.SigningScheme) ([]byte, error)
}

// sourceFor returns the source a plugins: entry names, or nil for an entry
// with no package (a cmd: entry).
func sourceFor(spec config.PluginSpec, dir string) source { //nolint:ireturn // file, oci and http are genuinely different concrete types
	switch {
	case spec.File != "":
		path := spec.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		return fileSource{path: path, checksum: spec.Checksum}
	case spec.OCI != "":
		return ociSource{ref: spec.OCI}
	case spec.HTTP != "":
		return httpSource{url: spec.HTTP, checksum: spec.Checksum}
	}
	return nil
}

// fileSource is a package on the local filesystem, with its signature files
// beside it.
type fileSource struct {
	path     string
	checksum string
}

func (s fileSource) fetch(context.Context) (pkgcache.Entry, error) {
	return pkgcache.Entry{Path: s.path, Digest: s.checksum}, nil
}

func (s fileSource) signature(_ context.Context, f pkgcache.Entry, scheme config.SigningScheme) ([]byte, error) {
	sigPath := f.Path + signatureSuffix(scheme)
	data, err := os.ReadFile(sigPath) //nolint:gosec // the path is a configured plugin package path, the whole point
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%q: %w", sigPath, pkgtrust.ErrSignatureMissing)
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", sigPath, err)
	}
	return data, nil
}

// ociSource is a package published to an OCI registry. Its signatures are
// artifacts at the cosign-style fallback tag its digest names.
type ociSource struct {
	ref string
}

func (s ociSource) fetch(ctx context.Context) (pkgcache.Entry, error) {
	return ocipkg.Fetch(ctx, s.ref)
}

func (s ociSource) signature(ctx context.Context, f pkgcache.Entry, scheme config.SigningScheme) ([]byte, error) {
	mediaType := ocipkg.SignatureMediaType
	if scheme == config.SigningSchemeSigstore {
		mediaType = ocipkg.SigstoreSignatureMediaType
	}
	return ocipkg.FetchSignature(ctx, s.ref, f.Digest, mediaType)
}

// httpSource is a package served over a plain URL, with its signature files
// at the same URL plus a suffix.
type httpSource struct {
	url      string
	checksum string
}

func (s httpSource) fetch(ctx context.Context) (pkgcache.Entry, error) {
	return httppkg.Fetch(ctx, s.url, s.checksum)
}

func (s httpSource) signature(ctx context.Context, _ pkgcache.Entry, scheme config.SigningScheme) ([]byte, error) {
	return httppkg.FetchSignature(ctx, s.url, signatureSuffix(scheme))
}

func signatureSuffix(scheme config.SigningScheme) string {
	if scheme == config.SigningSchemeSigstore {
		return sigstoreSuffix
	}
	return minisigSuffix
}
