package kubernetes

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestWantsRelay(t *testing.T) {
	assert.False(t, wantsRelay(config{}), "no expose entries and relay unset means no relay")
	assert.True(t, wantsRelay(config{Expose: map[string]expose{"a": {Address: "x:1"}}}))
	assert.True(t, wantsRelay(config{Relay: true}), "relay:true stands up the pod even with no expose entries")
	assert.False(t, wantsRelay(config{Relay: false}))
}

func TestReadRelayPort(t *testing.T) {
	t.Run("no relay address file yet", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		port, ok := readRelayPort(kubeconfig)
		assert.False(t, ok)
		assert.Zero(t, port)
	})

	t.Run("a valid relay address", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(relayAddrFile(kubeconfig), []byte("127.0.0.1:54321"), 0o600))

		port, ok := readRelayPort(kubeconfig)
		require.True(t, ok)
		assert.Equal(t, 54321, port)
	})

	t.Run("a malformed relay address", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(relayAddrFile(kubeconfig), []byte("not-a-host-port"), 0o600))

		port, ok := readRelayPort(kubeconfig)
		assert.False(t, ok)
		assert.Zero(t, port)
	})
}

func TestRelayUDPPorts(t *testing.T) {
	t.Run("no file yet is not reusable", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		ports, ok := readRelayUDPPorts(kubeconfig)
		assert.False(t, ok)
		assert.Nil(t, ports)
	})

	t.Run("round trips a pool through write and read", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, writeRelayUDPPorts(kubeconfig, []int{40000, 40001, 40002}))

		ports, ok := readRelayUDPPorts(kubeconfig)
		require.True(t, ok)
		assert.Equal(t, []int{40000, 40001, 40002}, ports)
	})

	t.Run("an empty pool is a valid, reusable state", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, writeRelayUDPPorts(kubeconfig, nil))

		ports, ok := readRelayUDPPorts(kubeconfig)
		require.True(t, ok)
		assert.Empty(t, ports)
	})

	t.Run("a malformed file is not reusable", func(t *testing.T) {
		kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
		require.NoError(t, os.WriteFile(relayUDPAddrFile(kubeconfig), []byte("not,a,port,list"), 0o600))

		ports, ok := readRelayUDPPorts(kubeconfig)
		assert.False(t, ok)
		assert.Nil(t, ports)
	})
}

func TestExposedViaRelay(t *testing.T) {
	t.Run("builds a socks5 upstream per entry, sorted by name", func(t *testing.T) {
		got, err := exposedViaRelay(map[string]expose{
			"postgres":   {Address: "postgres.default.svc:5432", Protocol: "tcp"},
			"kubernetes": {Address: "kubernetes.default.svc:443", Protocol: "tcp"},
		}, "127.0.0.1:54321", nil)
		require.NoError(t, err)

		require.Len(t, got, 2)
		assert.Equal(t, plugin.ExposedPort{
			Name: "kubernetes", Protocol: "tcp", Relay: true,
			Upstream: "socks5://127.0.0.1:54321/kubernetes.default.svc:443",
		}, got[0])
		assert.Equal(t, plugin.ExposedPort{
			Name: "postgres", Protocol: "tcp", Relay: true,
			Upstream: "socks5://127.0.0.1:54321/postgres.default.svc:5432",
		}, got[1])
	})

	t.Run("entries convert to card details", func(t *testing.T) {
		got, err := exposedViaRelay(map[string]expose{"postgres": {Address: "postgres.default.svc:5432", Protocol: "tcp"}}, "127.0.0.1:54321", nil)
		require.NoError(t, err)

		require.Len(t, got, 1)
		assert.Equal(t, plugin.Detail{
			Label: "tcp postgres (relay)", Value: plugin.String("socks5://127.0.0.1:54321/postgres.default.svc:5432"), Copyable: true,
		}, got[0].Detail(), "Up must mirror every exposed port onto the card the same way")
	})

	t.Run("threads host_port through to the exposed port", func(t *testing.T) {
		got, err := exposedViaRelay(map[string]expose{
			"postgres": {Address: "postgres.default.svc:5432", Protocol: "tcp", HostPort: 15432},
		}, "127.0.0.1:54321", nil)
		require.NoError(t, err)

		require.Len(t, got, 1)
		assert.Equal(t, 15432, got[0].HostPort)
	})

	t.Run("attaches the relay's udp pool to a udp entry", func(t *testing.T) {
		udpAddrs := map[string]string{"40000": "127.0.0.1:41000"}
		got, err := exposedViaRelay(map[string]expose{
			"dns": {Address: "kube-dns.kube-system.svc:53", Protocol: "udp"},
		}, "127.0.0.1:54321", udpAddrs)
		require.NoError(t, err)

		require.Len(t, got, 1)
		assert.Equal(t, plugin.ExposedPort{
			Name: "dns", Protocol: "udp", Relay: true,
			Upstream: "socks5://127.0.0.1:54321/kube-dns.kube-system.svc:53", RelayUDPAddrs: udpAddrs,
		}, got[0])
	})

	t.Run("a udp entry with no udp pool errors", func(t *testing.T) {
		_, err := exposedViaRelay(map[string]expose{
			"dns": {Address: "kube-dns.kube-system.svc:53", Protocol: "udp"},
		}, "127.0.0.1:54321", nil)
		require.ErrorIs(t, err, ErrNoRelayUDPPool)
	})
}

func TestExposedPortDetails(t *testing.T) {
	exposed := []plugin.ExposedPort{
		{Name: "postgres", Protocol: "socks5", Upstream: "socks5://127.0.0.1:54321/postgres.default.svc:5432"},
	}

	got := exposedPortDetails(exposed)

	require.Len(t, got, 1)
	assert.Equal(t, exposed[0].Detail(), got[0], "Up must mirror every exposed port onto the card the same way")
}

func TestSaveImageToTempFile(t *testing.T) {
	t.Run("writes the image stream to a temp file", func(t *testing.T) {
		rt := fakeRuntime{save: func(context.Context, string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("fake tar contents")), nil
		}}

		path, err := saveImageToTempFile(t.Context(), rt, "kevin-relay:dev")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.Remove(path) })

		data, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, "fake tar contents", string(data))
	})

	t.Run("the save failing is an error", func(t *testing.T) {
		rt := fakeRuntime{save: func(context.Context, string) (io.ReadCloser, error) {
			return nil, errors.New("no such image")
		}}

		_, err := saveImageToTempFile(t.Context(), rt, "kevin-relay:dev")
		require.Error(t, err)
	})
}

func TestDeployRelay(t *testing.T) {
	t.Run("saving the relay image fails before the driver ever loads it", func(t *testing.T) {
		rt := fakeRuntime{save: func(context.Context, string) (io.ReadCloser, error) {
			return nil, errors.New("no such image")
		}}
		loaded := false
		drv := fakeDriver{loadImage: func(context.Context, string) error { loaded = true; return nil }}

		err := deployRelay(t.Context(), rt, drv, "kevin-relay:dev", 0, &capture{})
		require.Error(t, err)
		assert.False(t, loaded)
	})

	t.Run("loading the image fails", func(t *testing.T) {
		drv := fakeDriver{loadImage: func(context.Context, string) error { return errors.New("load failed") }}

		err := deployRelay(t.Context(), fakeRuntime{}, drv, "kevin-relay:dev", 0, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load the relay image")
	})

	t.Run("applies the pod pinned to the control-plane node, then waits for it", func(t *testing.T) {
		var manifest string
		var waited bool
		drv := fakeDriver{
			kubectlInput: func(_ context.Context, stdin io.Reader, _ ...string) (string, error) {
				b, err := io.ReadAll(stdin)
				manifest = string(b)
				return "", err
			},
			kubectl: func(_ context.Context, args ...string) (string, error) {
				waited = slices.Contains(args, "wait")
				return "", nil
			},
		}

		require.NoError(t, deployRelay(t.Context(), fakeRuntime{}, drv, "kevin-relay:dev", 0, &capture{}))
		assert.Contains(t, manifest, "demo-cluster-control-plane")
		assert.True(t, waited)
	})
}

func TestFinishRelay(t *testing.T) {
	t.Run("propagates a deployRelay failure instead of reporting exposed ports", func(t *testing.T) {
		drv := fakeDriver{loadImage: func(context.Context, string) error { return errors.New("load failed") }}

		_, err := finishRelay(t.Context(), fakeRuntime{}, drv, config{}, "127.0.0.1:54321", nil, &capture{})
		require.Error(t, err)
	})
}
