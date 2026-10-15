package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/session"
)

func TestStatus(t *testing.T) {
	t.Run("reports project, network, proxy address, and every step", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.SetProxyAddr("127.0.0.1:8080")
		store.AddStep("web", "Web", "resource", "builtin", nil, nil, false, "", false)
		store.SetStep("web", session.Ready, "")

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/status", nil))

		require.Equal(t, 200, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var out runapi.Status
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
		assert.Equal(t, "demo", out.Project)
		assert.Equal(t, "kevin-demo", out.Network)
		assert.Equal(t, "127.0.0.1:8080", out.ProxyAddr)
		require.Len(t, out.Steps, 1)
		assert.Equal(t, "web", out.Steps[0].Name)
		assert.Equal(t, "ready", out.Steps[0].State)
	})

	t.Run("reports an empty step list rather than null", func(t *testing.T) {
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: session.NewStore()})

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/status", nil))

		require.Equal(t, 200, rec.Code)
		assert.JSONEq(t, `{"project":"demo","network":"kevin-demo","steps":[]}`, rec.Body.String())
	})
}

func TestAPIRerun(t *testing.T) {
	post := func(s *Server, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/rerun", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	withRerun := func(fn func(context.Context, runapi.RerunRequest) (runapi.RerunResult, error)) *Server {
		return New(Config{Project: "demo", Store: session.NewStore(), Rerun: fn})
	}

	t.Run("is unavailable without a rerun func", func(t *testing.T) {
		s := New(Config{Project: "demo", Store: session.NewStore()})

		assert.Equal(t, http.StatusServiceUnavailable, post(s, `{"step":"web"}`).Code)
	})

	t.Run("passes the request on and writes the result", func(t *testing.T) {
		var got runapi.RerunRequest
		s := withRerun(func(_ context.Context, req runapi.RerunRequest) (runapi.RerunResult, error) {
			got = req
			return runapi.RerunResult{Step: req.Step, Failed: true, Changed: []runapi.StepSummary{{Name: "web", State: "failed"}}}, nil
		})

		rec := post(s, `{"step":"web","cascade":true}`)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, runapi.RerunRequest{Step: "web", Cascade: true}, got)
		assert.JSONEq(t, `{"step":"web","failed":true,"changed":[{"name":"web","state":"failed","idempotent":false}]}`, rec.Body.String())
	})

	t.Run("maps a step that did not start to a status", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
			want int
		}{
			{name: "busy", err: session.ErrStepBusy, want: http.StatusConflict},
			{name: "unknown", err: session.ErrUnknownStep, want: http.StatusNotFound},
			{name: "anything else", err: errors.New("boom"), want: http.StatusInternalServerError},
		}
		for _, tt := range tests {
			s := withRerun(func(context.Context, runapi.RerunRequest) (runapi.RerunResult, error) {
				return runapi.RerunResult{}, tt.err
			})

			assert.Equal(t, tt.want, post(s, `{"step":"web"}`).Code, tt.name)
		}
	})

	t.Run("rejects a request that is not application/json", func(t *testing.T) {
		s := withRerun(func(context.Context, runapi.RerunRequest) (runapi.RerunResult, error) {
			t.Error("rerun must not run")
			return runapi.RerunResult{}, nil
		})
		req := httptest.NewRequestWithContext(t.Context(), "POST", "/api/rerun", strings.NewReader(`{"step":"web"}`))
		req.Header.Set("Content-Type", "text/plain")
		rec := httptest.NewRecorder()

		s.Handler().ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
	})

	t.Run("rejects a body that is not JSON", func(t *testing.T) {
		s := withRerun(func(context.Context, runapi.RerunRequest) (runapi.RerunResult, error) {
			t.Error("rerun must not run")
			return runapi.RerunResult{}, nil
		})

		assert.Equal(t, http.StatusBadRequest, post(s, `step=web`).Code)
	})
}
