package console

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/session"
)

func TestStatus(t *testing.T) {
	t.Run("reports project, network, proxy address, and every step", func(t *testing.T) {
		store := session.NewStore()
		s := New(Config{Project: "demo", Network: "kevin-demo", Store: store})
		store.SetProxyAddr("127.0.0.1:8080")
		store.AddStep("web", "Web", "resource", "builtin", nil, nil, false, "", false)
		store.SetStep("web", Ready, "")

		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), "GET", "/api/status", nil))

		require.Equal(t, 200, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

		var out StatusResponse
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
