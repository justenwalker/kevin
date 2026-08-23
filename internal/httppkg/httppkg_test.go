package httppkg_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/pkgcache"
)

func serve(t *testing.T, body []byte, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestFetch(t *testing.T) {
	t.Run("downloads and caches with no checksum", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		body := []byte("plugin package bytes")
		srv := serve(t, body, http.StatusOK)
		t.Cleanup(srv.Close)

		pkgPath, digestStr, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+sha256Hex(body), digestStr)
		got, err := os.ReadFile(pkgPath)
		require.NoError(t, err)
		assert.Equal(t, body, got)
		assert.Equal(t, pkgcache.Path(sha256Hex(body)), pkgPath, "the blob must be cached under its computed digest")
	})

	t.Run("verifies a matching checksum", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		body := []byte("plugin package bytes")
		srv := serve(t, body, http.StatusOK)
		t.Cleanup(srv.Close)

		_, digestStr, err := httppkg.Fetch(t.Context(), srv.URL, "sha256:"+sha256Hex(body))
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+sha256Hex(body), digestStr)
	})

	t.Run("rejects a mismatched checksum", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		body := []byte("plugin package bytes")
		srv := serve(t, body, http.StatusOK)
		t.Cleanup(srv.Close)

		_, _, err := httppkg.Fetch(t.Context(), srv.URL, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
		require.ErrorIs(t, err, httppkg.ErrChecksumMismatch)
		assert.NoFileExists(t, pkgcache.Path(sha256Hex(body)), "a mismatched download must not reach the cache")
	})

	t.Run("skips the network when the checksum is already cached", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		body := []byte("already have this one")
		wantHex := sha256Hex(body)

		cachePath := pkgcache.Path(wantHex)
		require.NoError(t, os.MkdirAll(filepath.Dir(cachePath), 0o700))
		require.NoError(t, os.WriteFile(cachePath, body, 0o600))

		var touched atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			touched.Store(true)
		}))
		t.Cleanup(srv.Close)

		pkgPath, digestStr, err := httppkg.Fetch(t.Context(), srv.URL, "sha256:"+wantHex)
		require.NoError(t, err)
		assert.False(t, touched.Load(), "Fetch must not touch the network when the checksum is already cached")
		assert.Equal(t, cachePath, pkgPath)
		assert.Equal(t, "sha256:"+wantHex, digestStr)
	})

	t.Run("rejects a non-success status", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		srv := serve(t, []byte("not found"), http.StatusNotFound)
		t.Cleanup(srv.Close)

		_, _, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.ErrorIs(t, err, httppkg.ErrFetch)
	})

	t.Run("rejects a bad URL", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		tests := []struct {
			name string
			url  string
		}{
			{name: "unparseable", url: "://not a url"},
			{name: "wrong scheme", url: "ftp://example.com/plugin.tar"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, _, err := httppkg.Fetch(t.Context(), tt.url, "")
				require.ErrorIs(t, err, httppkg.ErrBadURL)
			})
		}
	})

	t.Run("fails on an already canceled context", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		srv := serve(t, []byte("x"), http.StatusOK)
		t.Cleanup(srv.Close)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, _, err := httppkg.Fetch(ctx, srv.URL, "")
		require.ErrorIs(t, err, httppkg.ErrFetch)
	})

	t.Run("with no checksum, sends back the ETag it was given and skips the body on 304", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		body := []byte("plugin package bytes")
		const etag = `"v1"`

		var gotINM atomic.Value
		var calls atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			gotINM.Store(r.Header.Get("If-None-Match"))
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		pkgPath1, digest1, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+sha256Hex(body), digest1)
		assert.Equal(t, int32(1), calls.Load())
		assert.Empty(t, gotINM.Load(), "the first request carries no prior ETag")

		pkgPath2, digest2, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.NoError(t, err)
		assert.Equal(t, int32(2), calls.Load(), "a second Fetch must still make a conditional request")
		assert.Equal(t, etag, gotINM.Load(), "the second request must send back the recorded ETag")
		assert.Equal(t, pkgPath1, pkgPath2, "a 304 must resolve to the same cached blob")
		assert.Equal(t, digest1, digest2)
	})

	t.Run("with no checksum, a changed body on the next fetch replaces the cached digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		bodyV1 := []byte("version one")
		bodyV2 := []byte("version two")

		var body atomic.Pointer[[]byte]
		body.Store(&bodyV1)
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("ETag", `"current"`)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(*body.Load())
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		_, digest1, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+sha256Hex(bodyV1), digest1)

		body.Store(&bodyV2)
		pkgPath2, digest2, err := httppkg.Fetch(t.Context(), srv.URL, "")
		require.NoError(t, err)
		assert.Equal(t, "sha256:"+sha256Hex(bodyV2), digest2, "a 200 with a new body must update the cached digest")
		got, err := os.ReadFile(pkgPath2)
		require.NoError(t, err)
		assert.Equal(t, bodyV2, got)
	})
}

func TestFetchSignature(t *testing.T) {
	t.Run("downloads the .minisig sibling", func(t *testing.T) {
		sigBody := []byte("untrusted comment: test\nsignature bytes\n")
		mux := http.NewServeMux()
		mux.HandleFunc("/plugin.tar.gz.minisig", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(sigBody)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		got, err := httppkg.FetchSignature(t.Context(), srv.URL+"/plugin.tar.gz")
		require.NoError(t, err)
		assert.Equal(t, sigBody, got)
	})

	t.Run("reports a missing signature", func(t *testing.T) {
		srv := serve(t, []byte("not found"), http.StatusNotFound)
		t.Cleanup(srv.Close)

		_, err := httppkg.FetchSignature(t.Context(), srv.URL+"/plugin.tar.gz")
		require.ErrorIs(t, err, httppkg.ErrFetch)
	})

	t.Run("fails on an already canceled context", func(t *testing.T) {
		srv := serve(t, []byte("x"), http.StatusOK)
		t.Cleanup(srv.Close)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := httppkg.FetchSignature(ctx, srv.URL+"/plugin.tar.gz")
		require.ErrorIs(t, err, httppkg.ErrFetch)
	})
}
