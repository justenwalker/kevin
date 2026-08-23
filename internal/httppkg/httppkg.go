// Package httppkg fetches a kevin plugin package over HTTP(S) into the
// shared pkgcache, ready for internal/pluginpkg.Extract.
package httppkg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/justenwalker/kevin/internal/pkgcache"
)

// Fetch downloads rawURL and returns the local path of its cached
// plugin-package blob and its digest ("sha256:<hex>") - pass both straight
// into pluginpkg.Extract unmodified. If checksum is non-empty
// ("sha256:<hex>"), Fetch checks the cache for that digest before touching
// the network, and rejects a download that doesn't match it.
//
// With no checksum, Fetch instead sends the ETag/Last-Modified validators a
// prior Fetch of the same rawURL recorded, as If-None-Match/If-Modified-Since.
// A server that replies 304 Not Modified costs one round trip with no body,
// and Fetch reuses the cached blob from that prior fetch; any other status
// downloads and caches the new body, and records its own validators for next
// time.
func Fetch(ctx context.Context, rawURL, checksum string) (string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("httppkg: %q: %w", rawURL, ErrBadURL)
	}

	wantHex := checksumHex(checksum)
	if wantHex != "" && fileExists(pkgcache.Path(wantHex)) {
		return pkgcache.Path(wantHex), "sha256:" + wantHex, nil
	}

	var prior validators
	if wantHex == "" {
		if v, ok := loadValidators(rawURL); ok && fileExists(pkgcache.Path(v.Digest)) {
			prior = v
		}
	}
	return download(ctx, rawURL, wantHex, prior)
}

// FetchSignature downloads rawURL + ".minisig" - minisign's own default
// suffix for `minisign -S`/`-Sm` - and returns its raw bytes, ready for
// minisign.DecodeSignature. Unlike Fetch, the result is never cached: a
// signature file is tiny, and re-fetching it costs nothing.
func FetchSignature(ctx context.Context, rawURL string) ([]byte, error) {
	sigURL := rawURL + ".minisig"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sigURL, nil)
	if err != nil {
		return nil, fmt.Errorf("httppkg: %q: %w: %w", sigURL, ErrFetch, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("httppkg: %q: %w: %w", sigURL, ErrFetch, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only, the status/read errors below are what's reported

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("httppkg: %q: got status %d: %w", sigURL, resp.StatusCode, ErrFetch)
	}
	data, err := io.ReadAll(resp.Body) // a detached signature is a few hundred bytes
	if err != nil {
		return nil, fmt.Errorf("httppkg: read %q: %w: %w", sigURL, ErrFetch, err)
	}
	return data, nil
}

// checksumHex strips the "sha256:" prefix from checksum. A checksum that
// doesn't carry the prefix is returned unchanged rather than rejected here -
// it will simply never match a real digest.
func checksumHex(checksum string) string {
	hex, _ := strings.CutPrefix(checksum, "sha256:")
	return hex
}

// fileExists reports whether path names a file that Fetch can hand
// straight to a caller as-is.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// download fetches rawURL and streams it into the cache, atomically, only
// after its bytes hash to wantHex (when set) - a corrupt or tampered
// download never reaches the cache. When wantHex is empty, download sends
// prior's validators (if any) and, on a 304 Not Modified reply, reuses
// prior's cached blob instead of reading a body; otherwise it records the
// new response's validators for the next Fetch of the same rawURL.
func download(ctx context.Context, rawURL, wantHex string, prior validators) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("httppkg: %q: %w: %w", rawURL, ErrFetch, err)
	}
	if prior.ETag != "" {
		req.Header.Set("If-None-Match", prior.ETag)
	}
	if prior.LastModified != "" {
		req.Header.Set("If-Modified-Since", prior.LastModified)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("httppkg: %q: %w: %w", rawURL, ErrFetch, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only, download already reported its own errors

	if resp.StatusCode == http.StatusNotModified {
		return pkgcache.Path(prior.Digest), "sha256:" + prior.Digest, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("httppkg: %q: got status %d: %w", rawURL, resp.StatusCode, ErrFetch)
	}

	cachePath, gotHex, err := cacheBody(rawURL, resp.Body, wantHex)
	if err != nil {
		return "", "", err
	}

	if wantHex == "" {
		// Best effort: a server with no ETag/Last-Modified leaves this a
		// no-op, and a write failure here only costs the next Fetch its fast
		// path, not this one's already-downloaded result.
		saveValidators(rawURL, validators{
			Digest:       gotHex,
			ETag:         resp.Header.Get("ETag"),
			LastModified: resp.Header.Get("Last-Modified"),
		})
	}
	return cachePath, "sha256:" + gotHex, nil
}

// cacheBody streams body into the pkgcache directory, atomically, and
// returns its final cache path and hex digest. It rejects a body that
// doesn't hash to wantHex, when set, before the file ever lands in the
// cache.
func cacheBody(rawURL string, body io.Reader, wantHex string) (string, string, error) {
	dir := pkgcache.Sha256Dir()
	if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
		return "", "", fmt.Errorf("httppkg: create %q: %w", dir, mkdirErr)
	}
	tmp, err := os.CreateTemp(dir, "*.tmp") // same filesystem as the cache, for an atomic rename
	if err != nil {
		return "", "", fmt.Errorf("httppkg: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once renamed away

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), body); err != nil {
		tmp.Close() //nolint:errcheck,gosec // best effort; the copy error below is what's reported
		return "", "", fmt.Errorf("httppkg: download %q: %w", rawURL, err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("httppkg: close temp file: %w", err)
	}

	gotHex := hex.EncodeToString(h.Sum(nil))
	if wantHex != "" && gotHex != wantHex {
		return "", "", fmt.Errorf("httppkg: %q: got sha256:%s: %w", rawURL, gotHex, ErrChecksumMismatch)
	}

	cachePath := pkgcache.Path(gotHex)
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		return "", "", fmt.Errorf("httppkg: %w", err)
	}
	return cachePath, gotHex, nil
}

// validators are the conditional-GET fields a server returned for a prior
// download of one URL, recorded so the next Fetch of the same URL (with no
// checksum pinned) can ask the server "has this changed?" instead of always
// downloading the body again.
type validators struct {
	Digest       string `json:"digest"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// validatorsPath is where Fetch's recorded validators for rawURL live, keyed
// by the URL's own sha256 so two different URLs never collide.
func validatorsPath(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(pkgcache.Dir(), "http-meta", hex.EncodeToString(sum[:])+".json")
}

// loadValidators reads back a prior download's recorded validators for
// rawURL. It reports false when none are recorded, or the record doesn't
// parse - either way, the caller falls back to a plain download.
func loadValidators(rawURL string) (validators, bool) {
	data, err := os.ReadFile(validatorsPath(rawURL))
	if err != nil {
		return validators{}, false
	}
	var v validators
	if err := json.Unmarshal(data, &v); err != nil {
		return validators{}, false
	}
	return v, true
}

// saveValidators records v for rawURL, for a later Fetch to load. It ignores
// its own errors - see the comment at its call site in download.
func saveValidators(rawURL string, v validators) {
	path := validatorsPath(rawURL)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600)
}
