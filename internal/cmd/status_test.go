package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/runstate"
)

func TestPrintStatus(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, runstate.At(t.TempDir())))
		assert.Equal(t, "not running\n", buf.String())
	})

	t.Run("stale pidfile is cleared", func(t *testing.T) {
		state := runstate.At(t.TempDir())
		require.NoError(t, state.WritePID(deadPID(t)))

		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, state))
		assert.Equal(t, "not running (removed stale pid file)\n", buf.String())
		assert.Zero(t, state.PID())
	})

	t.Run("a live run prints its steps from the console", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/api/status", r.URL.Path)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"project":"demo","network":"kevin-demo",` +
				`"steps":[{"name":"web","state":"ready"},{"name":"db","state":"failed","message":"connect: refused"}]}`))
		}))
		t.Cleanup(srv.Close)

		state := runstate.At(t.TempDir())
		require.NoError(t, state.WritePID(os.Getpid()))
		require.NoError(t, state.WriteAddrs(runstate.Addrs{ConsoleAddr: strings.TrimPrefix(srv.URL, "http://")}))

		var buf bytes.Buffer
		require.NoError(t, printStatus(t.Context(), &buf, state))
		out := buf.String()
		assert.Contains(t, out, "web")
		assert.Contains(t, out, "ready")
		assert.Contains(t, out, "db")
		assert.Contains(t, out, "failed")
		assert.Contains(t, out, "connect: refused")
	})
}
