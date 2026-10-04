package netca

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func get(t *testing.T, tr *http.Transport, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func TestTransport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)

	t.Run("unset trusts only the system roots", func(t *testing.T) {
		t.Setenv(EnvVar, "")
		tr, err := Transport()
		require.NoError(t, err)
		assert.Error(t, get(t, tr, srv.URL))
	})

	t.Run("a CA file adds a root", func(t *testing.T) {
		t.Setenv(EnvVar, writeCert(t, srv))
		tr, err := Transport()
		require.NoError(t, err)
		assert.NoError(t, get(t, tr, srv.URL))
	})

	t.Run("a missing file is an error", func(t *testing.T) {
		t.Setenv(EnvVar, filepath.Join(t.TempDir(), "absent.pem"))
		_, err := Transport()
		require.ErrorIs(t, err, ErrCAFile)
	})

	t.Run("a file with no certificate is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "junk.pem")
		require.NoError(t, os.WriteFile(path, []byte("not a cert"), 0o600))
		t.Setenv(EnvVar, path)
		_, err := Transport()
		require.ErrorIs(t, err, ErrCAFile)
	})

	t.Run("the default transport is left alone", func(t *testing.T) {
		t.Setenv(EnvVar, writeCert(t, srv))
		_, err := Transport()
		require.NoError(t, err)
		def, ok := http.DefaultTransport.(*http.Transport)
		require.True(t, ok)
		assert.Error(t, get(t, def, srv.URL))
	})
}
