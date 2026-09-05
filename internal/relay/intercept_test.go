package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAddIntercept(t *testing.T) {
	t.Run("posts the host and ports as JSON", func(t *testing.T) {
		var got interceptRequest
		var gotMethod, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer srv.Close()

		r := &Relay{controlAddr: srv.Listener.Addr().String()}
		err := r.AddIntercept(t.Context(), "s3.us-east-1.amazonaws.com", []int{443})
		require.NoError(t, err)
		assert.Equal(t, http.MethodPost, gotMethod)
		assert.Equal(t, "/intercept", gotPath)
		assert.Equal(t, interceptRequest{Host: "s3.us-east-1.amazonaws.com", Ports: []int{443}}, got)
	})

	t.Run("a non-204 response is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		r := &Relay{controlAddr: srv.Listener.Addr().String()}
		err := r.AddIntercept(t.Context(), "s3.us-east-1.amazonaws.com", []int{443})
		require.ErrorIs(t, err, ErrInterceptRejected)
	})
}
