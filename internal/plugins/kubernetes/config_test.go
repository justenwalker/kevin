package kubernetes

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestResolveMounts(t *testing.T) {
	in := []mount{{Host: "src", Container: "/workspace"}, {Host: "/abs", Container: "/abs", ReadOnly: true}}

	got := resolveMounts(in, "/proj")

	assert.Equal(t, []mount{
		{Host: "/proj/src", Container: "/workspace"},
		{Host: "/abs", Container: "/abs", ReadOnly: true},
	}, got)
	assert.Equal(t, "src", in[0].Host, "the input is left alone")
}

func TestDecode(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)

		assert.Equal(t, "5m", cfg.Wait, "a cluster takes minutes")
		assert.True(t, cfg.Proxy)
		assert.True(t, cfg.CoreDNS, "a pod resolves a step unless a step opts out")
		assert.True(t, cfg.TrustCA, "a pull through the proxy must verify unless a step opts out")
		assert.Empty(t, cfg.Workers, "one control plane node is enough by default")
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := decode([]byte(`{`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode config")
	})

	t.Run("coredns opt-out", func(t *testing.T) {
		cfg, err := decode([]byte(`{"coredns":false}`))
		require.NoError(t, err)
		assert.False(t, cfg.CoreDNS, "a step must be able to opt out of the coredns patch")
	})

	t.Run("trust_ca opt-out", func(t *testing.T) {
		cfg, err := decode([]byte(`{"trust_ca":false}`))
		require.NoError(t, err)
		assert.False(t, cfg.TrustCA, "a step must be able to opt out of the certificate install")
	})

	t.Run("reads expose", func(t *testing.T) {
		cfg, err := decode([]byte(`{"expose":{"postgres":{"address":"postgres.default.svc:5432","host_port":15432}}}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 1)
		assert.Equal(t, expose{Address: "postgres.default.svc:5432", Protocol: "tcp", HostPort: 15432}, cfg.Expose["postgres"])
	})

	t.Run("reads an explicit udp expose protocol", func(t *testing.T) {
		cfg, err := decode([]byte(`{"expose":{"dns":{"address":"kube-dns.kube-system.svc:53","protocol":"udp"}}}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 1)
		assert.Equal(t, "udp", cfg.Expose["dns"].Protocol)
	})

	t.Run("retain is off by default", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)
		assert.False(t, cfg.Retain, "a failed cluster is removed unless a step keeps it")
	})

	t.Run("reads the k3d block", func(t *testing.T) {
		cfg, err := decode([]byte(`{"driver":"k3d","k3d":{"image":"rancher/k3s:v1.34.1-k3s1"}}`))
		require.NoError(t, err)

		assert.Equal(t, "k3d", cfg.Driver)
		assert.Equal(t, "rancher/k3s:v1.34.1-k3s1", cfg.K3d.Image)
	})

	t.Run("reads the minikube options", func(t *testing.T) {
		cfg, err := decode([]byte(`{"driver":"minikube","minikube":{"kubernetes_version":"v1.33.1","base_image":"kicbase:v1","memory":"2g","cpus":2}}`))
		require.NoError(t, err)

		assert.Equal(t, "minikube", cfg.Driver)
		assert.Equal(t, minikubeConfig{KubernetesVersion: "v1.33.1", BaseImage: "kicbase:v1", Memory: "2g", CPUs: 2}, cfg.Minikube)
	})

	t.Run("reads the k3d options", func(t *testing.T) {
		cfg, err := decode([]byte(`{"driver":"k3d","k3d":{"disable":["traefik","servicelb"],"env":{"A":"1"},"memory":"2g","labels":{"team":"x"}}}`))
		require.NoError(t, err)

		assert.Equal(t, []string{"traefik", "servicelb"}, cfg.K3d.Disable)
		assert.Equal(t, map[string]string{"A": "1"}, cfg.K3d.Env)
		assert.Equal(t, "2g", cfg.K3d.Memory)
		assert.Equal(t, map[string]string{"team": "x"}, cfg.K3d.Labels)
	})

	t.Run("reads mounts", func(t *testing.T) {
		cfg, err := decode([]byte(`{"mounts":[{"host":"src","container":"/workspace"},{"host":"/d","container":"/d","readonly":true}]}`))
		require.NoError(t, err)

		assert.Equal(t, []mount{
			{Host: "src", Container: "/workspace"},
			{Host: "/d", Container: "/d", ReadOnly: true},
		}, cfg.Mounts)
	})

	t.Run("reads workers", func(t *testing.T) {
		cfg, err := decode([]byte(`{"workers":{"worker_a":{},"worker_b":{}}}`))
		require.NoError(t, err)

		assert.Equal(t, map[string]map[string]any{"worker_a": {}, "worker_b": {}}, cfg.Workers)
	})

	t.Run("reads the kind control_plane and worker passthrough", func(t *testing.T) {
		cfg, err := decode([]byte(`{"kind":{"control_plane":{"image":"a"}},"workers":{"worker_a":{"image":"b"}}}`))
		require.NoError(t, err)

		assert.Equal(t, map[string]any{"image": "a"}, cfg.Kind.ControlPlane)
		assert.Equal(t, map[string]map[string]any{"worker_a": {"image": "b"}}, cfg.Workers)
	})
}

func TestClusterName(t *testing.T) {
	assert.Equal(t, "demo-cluster", clusterName(config{}, "demo", "cluster"))
	assert.Equal(t, "chosen", clusterName(config{Name: "chosen"}, "demo", "cluster"),
		"an explicit name wins")

	// Two projects must not share a cluster.
	assert.NotEqual(t,
		clusterName(config{}, "one", "cluster"),
		clusterName(config{}, "two", "cluster"))
}

func TestProxyEnv(t *testing.T) {
	env := plugin.Env{ProxyEnv: map[string]string{
		"HTTP_PROXY": "http://kevin:8080",
		"NO_PROXY":   "localhost",
	}}

	t.Run("passes the environment's proxy vars through when proxy is on", func(t *testing.T) {
		assert.Equal(t, env.ProxyEnv, proxyEnv(config{Proxy: true}, env.ProxyEnv))
	})

	t.Run("nil when the step opts out", func(t *testing.T) {
		assert.Nil(t, proxyEnv(config{Proxy: false}, env.ProxyEnv))
	})

	t.Run("nil when the environment has no proxy configured", func(t *testing.T) {
		assert.Nil(t, proxyEnv(config{Proxy: true}, nil))
	})
}
