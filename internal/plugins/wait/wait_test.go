package wait

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/plugin"
)

type capture struct{ logs []string }

func (c *capture) Log(_, text string)            { c.logs = append(c.logs, text) }
func (c *capture) Progress(string, int64, int64) {}

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	assert.NotEmpty(t, Step{}.Schema(), "Schema must return the embedded schema.cue")
}

func TestDecode(t *testing.T) {
	t.Run("applies the same defaults as the schema", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.Equal(t, "60s", cfg.Timeout)
		assert.Equal(t, "1s", cfg.Interval)
		assert.Nil(t, cfg.TCP)
	})

	t.Run("keeps the default when a field is omitted from JSON", func(t *testing.T) {
		cfg, err := decode([]byte(`{"tcp":{"address":"127.0.0.1:1"}}`))
		require.NoError(t, err)
		assert.Equal(t, "60s", cfg.Timeout, "an omitted interval/timeout key must keep the Go-side default, not become an empty string")
		assert.Equal(t, "1s", cfg.Interval)
	})

	t.Run("applies HTTP defaults when omitted", func(t *testing.T) {
		cfg, err := decode([]byte(`{"http":{"url":"http://example.com"}}`))
		require.NoError(t, err)
		require.NotNil(t, cfg.HTTP)
		assert.Equal(t, http.MethodGet, cfg.HTTP.Method, "an omitted http.method must keep the Go-side default, not become empty")
		assert.Equal(t, http.StatusOK, cfg.HTTP.Status, "an omitted http.status must keep the Go-side default, not become 0")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte("{"))
		assert.Error(t, err, "expected an error for malformed JSON")
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     config
		wantErr error
	}{
		{name: "rejects zero checks", cfg: config{}, wantErr: ErrCheck},
		{
			name:    "rejects multiple checks",
			cfg:     config{TCP: &tcpConfig{Address: "a"}, HTTP: &httpConfig{URL: "b"}},
			wantErr: ErrCheck,
		},
		{name: "accepts exactly one check", cfg: config{TCP: &tcpConfig{Address: "a"}}},
		{name: "accepts a duration", cfg: config{Duration: "5ms"}},
		{
			name:    "kubectl mode rejects zero of for/rollout",
			cfg:     config{Kubectl: &kubectlConfig{Resource: "pod/x"}},
			wantErr: ErrKubectlMode,
		},
		{
			name:    "kubectl mode rejects both for and rollout",
			cfg:     config{Kubectl: &kubectlConfig{Resource: "pod/x", For: "condition=Ready", Rollout: true}},
			wantErr: ErrKubectlMode,
		},
		{name: "kubectl mode accepts for", cfg: config{Kubectl: &kubectlConfig{Resource: "pod/x", For: "condition=Ready"}}},
		{name: "kubectl mode accepts rollout", cfg: config{Kubectl: &kubectlConfig{Resource: "pod/x", Rollout: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.cfg)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestRetry(t *testing.T) {
	t.Run("succeeds on first attempt", func(t *testing.T) {
		calls := 0
		err := retry(t.Context(), &capture{}, "x", time.Now().Add(time.Second), time.Millisecond, func(context.Context) error {
			calls++
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
	})

	t.Run("succeeds after failures", func(t *testing.T) {
		calls := 0
		err := retry(t.Context(), &capture{}, "x", time.Now().Add(time.Second), time.Millisecond, func(context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not yet")
			}
			return nil
		})
		require.NoError(t, err)
		assert.Equal(t, 3, calls)
	})

	t.Run("returns ErrTimeout after the deadline", func(t *testing.T) {
		err := retry(t.Context(), &capture{}, "x", time.Now().Add(-time.Second), time.Millisecond, func(context.Context) error {
			return errors.New("never ready")
		})
		require.ErrorIs(t, err, ErrTimeout)
		assert.Equal(t, "timed out waiting for x to become ready - check the step's logs, or raise timeout in kevin.cue",
			uerr.Display(err))
	})

	t.Run("returns the context error when canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		err := retry(ctx, &capture{}, "x", time.Now().Add(time.Second), time.Millisecond, func(context.Context) error {
			return errors.New("not yet")
		})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestDialTCP(t *testing.T) {
	t.Run("succeeds against a local listener", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()

		assert.NoError(t, dialTCP(t.Context(), ln.Addr().String()))
	})

	t.Run("fails when nothing listens", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		require.NoError(t, ln.Close())

		assert.Error(t, dialTCP(t.Context(), addr))
	})
}

func TestSplitSOCKS5(t *testing.T) {
	cases := []struct {
		name, address, relay, target string
		ok                           bool
	}{
		{name: "socks5 url splits relay and target", address: "socks5://127.0.0.1:1080/postgres:5432", relay: "127.0.0.1:1080", target: "postgres:5432", ok: true},
		{name: "plain host:port is not ok", address: "127.0.0.1:5432", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			relay, target, ok := splitSOCKS5(tc.address)
			assert.Equal(t, tc.ok, ok)
			if tc.ok {
				assert.Equal(t, tc.relay, relay)
				assert.Equal(t, tc.target, target)
			}
		})
	}
}

func TestProbeHTTP(t *testing.T) {
	t.Run("matches status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		defer srv.Close()

		assert.NoError(t, probeHTTP(t.Context(), httpConfig{URL: srv.URL, Method: http.MethodGet, Status: http.StatusOK}))
	})

	t.Run("rejects the wrong status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
		defer srv.Close()

		err := probeHTTP(t.Context(), httpConfig{URL: srv.URL, Method: http.MethodGet, Status: http.StatusOK})
		require.ErrorIs(t, err, ErrStatus)
	})
}

func TestRunExec(t *testing.T) {
	t.Run("succeeds", func(t *testing.T) {
		assert.NoError(t, runExec(t.Context(), []string{"sh", "-c", "exit 0"}))
	})

	t.Run("fails on a non-zero exit", func(t *testing.T) {
		assert.Error(t, runExec(t.Context(), []string{"sh", "-c", "exit 1"}))
	})
}

func TestUp(t *testing.T) {
	t.Run("rejects zero checks", func(t *testing.T) {
		_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Config: json.RawMessage(`{"timeout":"1s","interval":"1ms"}`),
		}, &capture{})
		assert.ErrorIs(t, err, ErrCheck)
	})

	t.Run("succeeds with a TCP check", func(t *testing.T) {
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = ln.Close() }()

		cfg := `{"timeout":"1s","interval":"1ms","tcp":{"address":"` + ln.Addr().String() + `"}}`
		_, err = Step{}.Up(t.Context(), &plugin.UpRequest{Config: json.RawMessage(cfg)}, &capture{})
		assert.NoError(t, err)
	})

	t.Run("succeeds with a duration", func(t *testing.T) {
		start := time.Now()
		_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Config: json.RawMessage(`{"duration":"20ms"}`),
		}, &capture{})
		require.NoError(t, err)
		assert.GreaterOrEqual(t, time.Since(start), 20*time.Millisecond)
	})

	t.Run("with a duration, stops when the context ends", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := Step{}.Up(ctx, &plugin.UpRequest{
			Config: json.RawMessage(`{"duration":"1m"}`),
		}, &capture{})
		assert.ErrorIs(t, err, ctx.Err())
	})

	t.Run("rejects a malformed duration", func(t *testing.T) {
		_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
			Config: json.RawMessage(`{"duration":"soon"}`),
		}, &capture{})
		assert.Error(t, err)
	})
}

func TestKindIsProbe(t *testing.T) {
	assert.Equal(t, plugin.StepKindProbe, Step{}.Kind(), "a wait step creates no resource")
}

func TestStepDoesNotImplementDowner(t *testing.T) {
	_, ok := any(Step{}).(plugin.Downer)
	assert.False(t, ok, "a wait step has nothing to clean up on teardown")
}

func TestStepIsIdempotent(t *testing.T) {
	assert.True(t, Step{}.Idempotent(), "a wait step creates nothing, so a rerun just checks or sleeps again")
}
