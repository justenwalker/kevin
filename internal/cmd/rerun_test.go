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

	"github.com/justenwalker/kevin/protos/pb"
)

func TestRerunStep(t *testing.T) {
	// consoleStub serves /api/status from states in order (one entry per
	// call) and answers the rerun POST with rerunCode.
	consoleStub := func(t *testing.T, rerunCode int, wantCascade string, states ...string) string {
		t.Helper()
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				assert.Equal(t, "/steps/web/rerun", r.URL.Path)
				assert.Equal(t, wantCascade, r.FormValue("cascade"))
				w.WriteHeader(rerunCode)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(states[min(calls, len(states)-1)]))
			calls++
		}))
		t.Cleanup(srv.Close)

		dir := t.TempDir()
		require.NoError(t, writePID(dir, os.Getpid()))
		require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: strings.TrimPrefix(srv.URL, "http://")}))
		return dir
	}
	status := func(webState, webMessage string) string {
		return `{"steps":[{"name":"web","state":"` + webState + `","message":"` + webMessage + `"},{"name":"db","state":"ready"}]}`
	}

	t.Run("prints the steps that changed", func(t *testing.T) {
		dir := consoleStub(t, http.StatusAccepted, "true", status("failed", "boom"), status("ready", ""))

		var buf bytes.Buffer
		require.NoError(t, rerunStep(t.Context(), &buf, dir, "web", true))
		assert.Equal(t, "web ready\n", buf.String())
	})

	t.Run("prints a changed dependent and hides an unchanged one", func(t *testing.T) {
		dir := consoleStub(t, http.StatusAccepted, "false",
			`{"steps":[{"name":"web","state":"ready"},{"name":"db","state":"skipped"},{"name":"cache","state":"ready"}]}`,
			`{"steps":[{"name":"web","state":"ready"},{"name":"db","state":"ready"},{"name":"cache","state":"ready"}]}`)

		var buf bytes.Buffer
		require.NoError(t, rerunStep(t.Context(), &buf, dir, "web", false))
		assert.Equal(t, "web ready\ndb ready\n", buf.String())
	})

	t.Run("fails when the console is unavailable", func(t *testing.T) {
		dir := consoleStub(t, http.StatusServiceUnavailable, "true", status("ready", ""))

		err := rerunStep(t.Context(), &bytes.Buffer{}, dir, "web", true)
		require.ErrorContains(t, err, "console returned 503")
	})

	t.Run("fails when the target ends failed", func(t *testing.T) {
		dir := consoleStub(t, http.StatusAccepted, "true", status("ready", ""), status("failed", "boom"))

		var buf bytes.Buffer
		err := rerunStep(t.Context(), &buf, dir, "web", true)
		require.ErrorIs(t, err, ErrRerunFailed)
		assert.Equal(t, "web failed: boom\n", buf.String())
	})

	t.Run("fails at once on a busy step", func(t *testing.T) {
		dir := consoleStub(t, http.StatusConflict, "true", status("running", ""))

		err := rerunStep(t.Context(), &bytes.Buffer{}, dir, "web", true)
		require.ErrorContains(t, err, `step "web" is busy`)
	})

	t.Run("fails on an unknown step", func(t *testing.T) {
		dir := consoleStub(t, http.StatusNotFound, "true", status("ready", ""))

		err := rerunStep(t.Context(), &bytes.Buffer{}, dir, "web", true)
		require.ErrorContains(t, err, `no step named "web"`)
	})

	t.Run("no pidfile", func(t *testing.T) {
		err := rerunStep(t.Context(), &bytes.Buffer{}, t.TempDir(), "web", false)
		require.ErrorIs(t, err, ErrNotRunning)
	})

	t.Run("stale pidfile", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writePID(dir, deadPID(t)))

		err := rerunStep(t.Context(), &bytes.Buffer{}, dir, "web", false)
		require.ErrorIs(t, err, ErrStaleRun)
	})
}
