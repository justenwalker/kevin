package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/runstate"
)

func TestRerunStep(t *testing.T) {
	// consoleStub answers POST /api/rerun with code and body, and checks the
	// request it received against want.
	consoleStub := func(t *testing.T, want runapi.RerunRequest, code int, body string) runstate.State {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, runapi.RerunPath, r.URL.Path)
			var got runapi.RerunRequest
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
			assert.Equal(t, want, got)
			w.WriteHeader(code)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)

		state := runstate.At(t.TempDir())
		require.NoError(t, state.WritePID(os.Getpid()))
		require.NoError(t, state.WriteAddrs(runstate.Addrs{ConsoleAddr: strings.TrimPrefix(srv.URL, "http://")}))
		return state
	}
	request := runapi.RerunRequest{Step: "web", Cascade: true}

	t.Run("prints each step that changed", func(t *testing.T) {
		state := consoleStub(t, request, http.StatusOK,
			`{"step":"web","changed":[{"name":"web","state":"ready"},{"name":"db","state":"ready","message":"back"}]}`)

		var buf bytes.Buffer
		require.NoError(t, rerunStep(t.Context(), &buf, state, "web", true))
		assert.Equal(t, "web ready\ndb ready: back\n", buf.String())
	})

	t.Run("fails when the console is unavailable", func(t *testing.T) {
		state := consoleStub(t, request, http.StatusServiceUnavailable, "")

		err := rerunStep(t.Context(), &bytes.Buffer{}, state, "web", true)
		require.ErrorContains(t, err, "console returned 503")
	})

	t.Run("fails when the target ends failed", func(t *testing.T) {
		state := consoleStub(t, request, http.StatusOK,
			`{"step":"web","failed":true,"changed":[{"name":"web","state":"failed","message":"boom"}]}`)

		var buf bytes.Buffer
		err := rerunStep(t.Context(), &buf, state, "web", true)
		require.ErrorIs(t, err, ErrRerunFailed)
		assert.Equal(t, "web failed: boom\n", buf.String())
	})

	t.Run("fails at once on a busy step", func(t *testing.T) {
		state := consoleStub(t, request, http.StatusConflict, "")

		err := rerunStep(t.Context(), &bytes.Buffer{}, state, "web", true)
		require.ErrorContains(t, err, `step "web" is busy`)
	})

	t.Run("fails on an unknown step", func(t *testing.T) {
		state := consoleStub(t, request, http.StatusNotFound, "")

		err := rerunStep(t.Context(), &bytes.Buffer{}, state, "web", true)
		require.ErrorContains(t, err, `no step named "web"`)
	})

	t.Run("no pidfile", func(t *testing.T) {
		err := rerunStep(t.Context(), &bytes.Buffer{}, runstate.At(t.TempDir()), "web", false)
		require.ErrorIs(t, err, runstate.ErrNotRunning)
	})

	t.Run("stale pidfile", func(t *testing.T) {
		state := runstate.At(t.TempDir())
		require.NoError(t, state.WritePID(deadPID(t)))

		err := rerunStep(t.Context(), &bytes.Buffer{}, state, "web", false)
		require.ErrorIs(t, err, runstate.ErrStaleRun)
	})
}
