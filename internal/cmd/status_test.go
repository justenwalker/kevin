package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestPrintStatus(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, t.TempDir()))
		assert.Equal(t, "not running\n", buf.String())
	})

	t.Run("stale pidfile is cleared", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writePID(dir, deadPID(t)))

		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, dir))
		assert.Equal(t, "not running (removed stale pid file)\n", buf.String())
		assert.NoFileExists(t, filepath.Join(dir, pidFileName))
	})

	t.Run("a live run prints its steps from the console", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/status", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"project":"demo","network":"kevin-demo",` +
				`"steps":[{"name":"web","state":"ready"},{"name":"db","state":"failed","message":"connect: refused"}]}`))
		}))
		t.Cleanup(srv.Close)

		dir := t.TempDir()
		require.NoError(t, writePID(dir, os.Getpid()))
		require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: strings.TrimPrefix(srv.URL, "http://")}))

		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, dir))
		out := buf.String()
		assert.Contains(t, out, "web")
		assert.Contains(t, out, "ready")
		assert.Contains(t, out, "db")
		assert.Contains(t, out, "failed")
		assert.Contains(t, out, "connect: refused")
	})
}

func TestFetchStatus(t *testing.T) {
	t.Run("errors on a non-200 response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		_, err := fetchStatus(t.Context(), strings.TrimPrefix(srv.URL, "http://"))
		require.Error(t, err)
	})

	t.Run("errors on a malformed response body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		t.Cleanup(srv.Close)

		_, err := fetchStatus(t.Context(), strings.TrimPrefix(srv.URL, "http://"))
		require.Error(t, err)
	})
}
