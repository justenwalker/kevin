package runapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/runstate"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/ui/console"
)

func dialServer(t *testing.T, h http.Handler) *runapi.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return runapi.Dial(runstate.Addrs{ConsoleAddr: strings.TrimPrefix(srv.URL, "http://")})
}

// TestClient runs the client against the real console handlers over HTTP.
// Integration tier: console and client together, no container engine.
func TestClient(t *testing.T) {
	store := session.NewStore()
	store.AddStep(session.Step{Name: "web", Label: "Web", Kind: "resource", Provider: "builtin"})
	store.SetStep("web", session.Ready, "")
	rerun := func(req runapi.RerunRequest) (runapi.RerunResult, error) {
		switch req.Step {
		case "busy":
			return runapi.RerunResult{}, session.ErrStepBusy
		case "nope":
			return runapi.RerunResult{}, session.ErrUnknownStep
		case "broken":
			return runapi.RerunResult{Step: req.Step, Failed: true, Changed: []runapi.StepSummary{{Name: "broken", State: "failed", Message: "boom"}}}, nil
		default:
			return runapi.RerunResult{Step: req.Step, Changed: []runapi.StepSummary{{Name: req.Step, State: "ready"}}}, nil
		}
	}
	c := dialServer(t, console.New(console.Config{
		Project: "demo", Network: "kevin-demo", Store: store,
		Rerun: func(_ context.Context, req runapi.RerunRequest) (runapi.RerunResult, error) { return rerun(req) },
	}).Handler())

	t.Run("Status reports the project and its steps", func(t *testing.T) {
		got, err := c.Status(t.Context())

		require.NoError(t, err)
		assert.Equal(t, "demo", got.Project)
		require.Len(t, got.Steps, 1)
		assert.Equal(t, "web", got.Steps[0].Name)
		assert.Equal(t, "ready", got.Steps[0].State)
	})

	t.Run("Rerun returns what changed", func(t *testing.T) {
		got, err := c.Rerun(t.Context(), runapi.RerunRequest{Step: "web", Cascade: true})

		require.NoError(t, err)
		assert.False(t, got.Failed)
		require.Len(t, got.Changed, 1)
		assert.Equal(t, "web", got.Changed[0].Name)
	})

	t.Run("Rerun reports a step that failed as a result", func(t *testing.T) {
		got, err := c.Rerun(t.Context(), runapi.RerunRequest{Step: "broken"})

		require.NoError(t, err)
		assert.True(t, got.Failed)
		assert.Equal(t, "boom", got.Changed[0].Message)
	})

	t.Run("Rerun maps 404 and 409 to sentinel errors", func(t *testing.T) {
		_, err := c.Rerun(t.Context(), runapi.RerunRequest{Step: "nope"})
		require.ErrorIs(t, err, runapi.ErrUnknownStep)

		_, err = c.Rerun(t.Context(), runapi.RerunRequest{Step: "busy"})
		require.ErrorIs(t, err, runapi.ErrBusy)
	})
}

func TestClientErrors(t *testing.T) {
	t.Run("a non-200 status", func(t *testing.T) {
		c := dialServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))

		_, err := c.Status(t.Context())

		require.ErrorContains(t, err, "console returned 500")
	})

	t.Run("a malformed body", func(t *testing.T) {
		c := dialServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))

		_, err := c.Rerun(t.Context(), runapi.RerunRequest{Step: "web"})

		require.ErrorContains(t, err, "decode response")
	})

	t.Run("nothing listening", func(t *testing.T) {
		c := runapi.Dial(runstate.Addrs{ConsoleAddr: "127.0.0.1:1"})

		_, err := c.Status(t.Context())

		require.Error(t, err)
	})
}
