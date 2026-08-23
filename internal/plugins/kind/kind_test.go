package kind

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/plugin"
)

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "workers")
}

func TestDecode(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg, err := decode(nil)
		require.NoError(t, err)

		assert.Equal(t, "5m", cfg.Wait, "a cluster takes minutes")
		assert.True(t, cfg.Proxy)
		assert.True(t, cfg.CoreDNS, "a pod resolves a step unless a step opts out")
		assert.True(t, cfg.TrustCA, "a pull through the proxy must verify unless a step opts out")
		assert.Equal(t, 0, cfg.Workers, "one control plane node is enough by default")
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
		cfg, err := decode([]byte(`{"expose":[{"address":"postgres.default.svc:5432","name":"postgres"}]}`))
		require.NoError(t, err)

		require.Len(t, cfg.Expose, 1)
		assert.Equal(t, kindExpose{Address: "postgres.default.svc:5432", Name: "postgres"}, cfg.Expose[0])
	})
}

func TestWantsRelay(t *testing.T) {
	assert.False(t, wantsRelay(config{}), "no expose entries and relay unset means no relay")
	assert.True(t, wantsRelay(config{Expose: []kindExpose{{Address: "x:1"}}}))
	assert.True(t, wantsRelay(config{Relay: true}), "relay:true stands up the pod even with no expose entries")
	assert.False(t, wantsRelay(config{Relay: false}))
}

func TestRelayAddr(t *testing.T) {
	assert.Equal(t, "127.0.0.1:54321", relayAddr(54321))
}

func TestWantsCoreDNSPatch(t *testing.T) {
	tests := []struct {
		name    string
		coredns bool
		relay   string
		domain  string
		want    bool
	}{
		{name: "relay and domain are both set", coredns: true, relay: "10.244.0.5:53", domain: "kevin.home", want: true},
		{name: "the relay is disabled for the environment", coredns: true, relay: "", domain: "kevin.home", want: false},
		{name: "the environment declares no domain", coredns: true, relay: "10.244.0.5:53", domain: "", want: false},
		{name: "the step opts out", coredns: false, relay: "10.244.0.5:53", domain: "kevin.home", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantsCoreDNSPatch(config{CoreDNS: tt.coredns},
				plugin.Env{Relay: tt.relay, Domain: tt.domain})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWantsTrustCA(t *testing.T) {
	tests := []struct {
		name    string
		trustCA bool
		caPEM   string
		want    bool
	}{
		{name: "trust_ca is on and the environment carries a certificate", trustCA: true, caPEM: "-----BEGIN CERTIFICATE-----", want: true},
		{name: "the environment carries no certificate", trustCA: true, caPEM: "", want: false},
		{name: "the step opts out", trustCA: false, caPEM: "-----BEGIN CERTIFICATE-----", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wantsTrustCA(config{TrustCA: tt.trustCA}, plugin.Env{CAPath: tt.caPEM})
			assert.Equal(t, tt.want, got)
		})
	}
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

func TestClusterConfig(t *testing.T) {
	t.Run("counts the nodes", func(t *testing.T) {
		tests := []struct {
			name    string
			workers int
			want    int
		}{
			{name: "control plane only", workers: 0, want: 1},
			{name: "one worker", workers: 1, want: 2},
			{name: "three workers", workers: 3, want: 4},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := clusterConfig(config{Workers: tt.workers}, 0)

				assert.Contains(t, got, "apiVersion: kind.x-k8s.io/v1alpha4")
				assert.Equal(t, 1, strings.Count(got, "role: control-plane"))
				assert.Equal(t, tt.workers, strings.Count(got, "role: worker"))
				assert.Equal(t, tt.want, strings.Count(got, "- role:"))
			})
		}
	})

	t.Run("prefers an explicit config", func(t *testing.T) {
		raw := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  apiServerPort: 6443\n"

		got := clusterConfig(config{Config: raw, Workers: 5}, 0)

		assert.Equal(t, raw, got)
		assert.NotContains(t, got, "role: worker", "workers is ignored when config is set")
	})

	t.Run("adds extra port mappings for the relay", func(t *testing.T) {
		without := clusterConfig(config{}, 0)
		assert.NotContains(t, without, "extraPortMappings", "no expose means no port mapping")

		with := clusterConfig(config{}, 54321)
		assert.Contains(t, with, "extraPortMappings")
		assert.Contains(t, with, "containerPort: 1080")
		assert.Contains(t, with, "hostPort: 54321")
		assert.Contains(t, with, `listenAddress: "127.0.0.1"`)
	})
}

func TestProxyEnv(t *testing.T) {
	env := plugin.Env{ProxyEnv: map[string]string{
		"HTTP_PROXY": "http://kevin:8080",
		"NO_PROXY":   "localhost",
	}}

	t.Run("passes the environment's proxy vars through when proxy is on", func(t *testing.T) {
		assert.Equal(t, env.ProxyEnv, proxyEnv(config{Proxy: true}, env))
	})

	t.Run("nil when the step opts out", func(t *testing.T) {
		assert.Nil(t, proxyEnv(config{Proxy: false}, env))
	})

	t.Run("nil when the environment has no proxy configured", func(t *testing.T) {
		assert.Nil(t, proxyEnv(config{Proxy: true}, plugin.Env{}))
	})
}

func TestUpReportsABadWait(t *testing.T) {
	_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   "cluster",
		Config: []byte(`{"wait":"soon"}`),
	}, &capture{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "wait")
}

func TestDownIsIdempotent(t *testing.T) {
	requireDocker(t)
	requireKind(t)

	// A cluster that never existed is not an error. The supervisor calls Down
	// for every step of the setup scope, present or not.
	out := &capture{}
	err := Step{}.Down(t.Context(), &plugin.DownRequest{
		Step: "cluster",
		Env: plugin.Env{
			Project:   "kevin-kind-absent",
			Workspace: t.TempDir(),
		},
	}, out)

	require.NoError(t, err)
	assert.Contains(t, strings.Join(out.stdout, "\n"), "removing cluster kevin-kind-absent-cluster")
}

func TestPickHostPort(t *testing.T) {
	port, err := findFreePort(t.Context())
	require.NoError(t, err)
	assert.Positive(t, port)
}

func TestExposedViaRelay(t *testing.T) {
	t.Run("builds a socks5 upstream per entry", func(t *testing.T) {
		got := exposedViaRelay([]kindExpose{
			{Address: "postgres.default.svc:5432", Name: "postgres"},
			{Address: "kubernetes.default.svc:443"},
		}, "127.0.0.1:54321")

		require.Len(t, got, 2)
		assert.Equal(t, plugin.ExposedPort{
			Name: "postgres", Protocol: "socks5",
			Upstream: "socks5://127.0.0.1:54321/postgres.default.svc:5432",
		}, got[0])
		assert.Equal(t, plugin.ExposedPort{
			Name: "kubernetes.default.svc:443", Protocol: "socks5",
			Upstream: "socks5://127.0.0.1:54321/kubernetes.default.svc:443",
		}, got[1], "an entry with no name defaults to its address")
	})

	t.Run("entries convert to card details", func(t *testing.T) {
		got := exposedViaRelay([]kindExpose{{Address: "postgres.default.svc:5432", Name: "postgres"}}, "127.0.0.1:54321")

		require.Len(t, got, 1)
		assert.Equal(t, plugin.Detail{
			Label: "socks5 postgres", Value: plugin.String("socks5://127.0.0.1:54321/postgres.default.svc:5432"), Copyable: true,
		}, got[0].Detail(), "Up must mirror every exposed port onto the card the same way")
	})
}

func TestRelayPodManifest(t *testing.T) {
	got := relayPodManifest("kind-example-control-plane", "kevin-relay:dev")

	assert.Contains(t, got, "nodeName: kind-example-control-plane")
	assert.Contains(t, got, "image: kevin-relay:dev")
	assert.Contains(t, got, "imagePullPolicy: Never")
	assert.Contains(t, got, `args: ["socks5-gateway", "--listen", ":1080"]`)
	assert.Contains(t, got, "containerPort: 1080")
	assert.Contains(t, got, "hostPort: 1080")
}

func TestBootstrapControlPlaneNode(t *testing.T) {
	t.Run("picks the single control-plane node", func(t *testing.T) {
		got, err := bootstrapControlPlaneNode([]string{"demo-control-plane", "demo-worker"})
		require.NoError(t, err)
		assert.Equal(t, "demo-control-plane", got)
	})

	t.Run("sorts ascending and picks the first for a hand-written HA config", func(t *testing.T) {
		got, err := bootstrapControlPlaneNode([]string{"demo-control-plane3", "demo-control-plane", "demo-control-plane2"})
		require.NoError(t, err)
		assert.Equal(t, "demo-control-plane", got)
	})

	t.Run("no control-plane node is an error", func(t *testing.T) {
		_, err := bootstrapControlPlaneNode([]string{"demo-worker"})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})
}

// requireDocker skips a test when the docker daemon does not answer.
func requireDocker(t *testing.T) {
	t.Helper()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
}

// requireKind skips a test when the kind binary does not answer.
func requireKind(t *testing.T) {
	t.Helper()
	if err := kindcmd.Available(t.Context()); err != nil {
		t.Skip("kind is unavailable:", err)
	}
}
