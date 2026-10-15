package engine

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/state"
)

func TestServingClose(t *testing.T) {
	t.Run("stops once and reports the same result every call", func(t *testing.T) {
		stops := 0
		done := make(chan error, 1)
		done <- assert.AnError
		s := &serving{stop: context.CancelFunc(func() { stops++ }), done: done}

		first := s.Close()
		second := s.Close()

		require.ErrorIs(t, first, assert.AnError)
		require.ErrorIs(t, second, assert.AnError)
		assert.Equal(t, 1, stops)
	})
}

// TestStartProxyReleasesListeners proves that a failed startProxy leaves no
// listener bound, using a loopback gateway so no docker daemon is needed.
func TestStartProxyReleasesListeners(t *testing.T) {
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())
	authority, err := ca.NewManager("cwd", "", "proxy-release", ca.Options{}).LoadOrGenerateIntermediate()
	require.NoError(t, err)

	tests := []struct {
		name string
		rt   func(t *testing.T) (gatewayRuntime, int)
	}{
		{
			name: "the gateway lookup fails",
			rt: func(*testing.T) (gatewayRuntime, int) {
				return gatewayRuntime{gatewayErr: assert.AnError}, 0
			},
		},
		{
			name: "the gateway port is already in use",
			rt: func(t *testing.T) (gatewayRuntime, int) {
				t.Helper()
				var lc net.ListenConfig
				held, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { _ = held.Close() })
				return gatewayRuntime{gateway: cri.Gateway{V4: netip.MustParseAddr("127.0.0.1")}}, mustPort(t, held.Addr().String())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, port := tt.rt(t)
			listen := freeLoopbackAddr(t)

			_, err := startProxy(t.Context(), rt, authority, proxyOptions{
				Network:     "net",
				Listen:      listen,
				GatewayPort: port,
				Domain:      "kevin.test",
			})
			require.Error(t, err)

			var lc net.ListenConfig
			again, err := lc.Listen(t.Context(), "tcp", listen)
			require.NoError(t, err, "the primary listener must be released")
			require.NoError(t, again.Close())
		})
	}
}

// TestStartProxyOnRecord proves the callback is live from the first request.
func TestStartProxyOnRecord(t *testing.T) {
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())
	authority, err := ca.NewManager("cwd", "", "proxy-record", ca.Options{}).LoadOrGenerateIntermediate()
	require.NoError(t, err)

	records := make(chan proxy.Record, 1)
	server, err := startProxy(t.Context(), gatewayRuntime{}, authority, proxyOptions{
		Network:  "net",
		Listen:   freeLoopbackAddr(t),
		Domain:   "kevin.test",
		Deny:     true,
		OnRecord: func(rec proxy.Record) { records <- rec },
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	proxyURL, err := url.Parse("http://" + server.addr)
	require.NoError(t, err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://blocked.example/", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	select {
	case <-records:
	case <-time.After(5 * time.Second):
		t.Fatal("OnRecord was not called")
	}
}
