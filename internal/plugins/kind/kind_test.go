package kind

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
	"go.yaml.in/yaml/v3"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/plugin"
)

// dockerClient inspects docker resources directly in tests - every test
// here runs against the default (docker) engine.
var dockerClient = docker.Client{}

// capture records what a step logs, as a fake plugin.Emitter.
type capture struct {
	stdout []string
	stderr []string
}

func (c *capture) Log(stream, text string) {
	if stream == "stderr" {
		c.stderr = append(c.stderr, text)
		return
	}
	c.stdout = append(c.stdout, text)
}

func (c *capture) Progress(string, int64, int64) {}

// fakeRuntime is a hand-written cri.Runtime double. It lets a test drive the
// kubectl-over-Exec orchestration in capture.go, coredns.go, and trustca.go
// without a live docker daemon or kind cluster.
type fakeRuntime struct {
	exec      func(ctx context.Context, container string, args ...string) (string, error)
	execInput func(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error)
	inspect   func(ctx context.Context, container string) (cri.Container, error)
	save      func(ctx context.Context, image string) (io.ReadCloser, error)
}

var _ cri.Runtime = fakeRuntime{}

func (f fakeRuntime) Exec(ctx context.Context, container string, args ...string) (string, error) {
	if f.exec == nil {
		return "", nil
	}
	return f.exec(ctx, container, args...)
}

func (f fakeRuntime) ExecInput(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error) {
	if f.execInput == nil {
		return "", nil
	}
	return f.execInput(ctx, container, stdin, args...)
}

func (f fakeRuntime) Inspect(ctx context.Context, name string) (cri.Container, error) {
	if f.inspect == nil {
		return cri.Container{}, nil
	}
	return f.inspect(ctx, name)
}

func (fakeRuntime) Available(context.Context) error { return nil }

func (fakeRuntime) Run(context.Context, cri.RunSpec) (string, error) { return "", nil }

func (fakeRuntime) Remove(context.Context, string) error { return nil }

func (f fakeRuntime) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	if f.save == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return f.save(ctx, image)
}

func (fakeRuntime) NetworkCreate(context.Context, string, cri.NetworkOptions) error { return nil }

func (fakeRuntime) NetworkRemove(context.Context, string) error { return nil }

func (fakeRuntime) NetworkConnect(context.Context, string, string) error { return nil }

func (fakeRuntime) NetworkGateway(context.Context, string) (cri.Gateway, error) {
	return cri.Gateway{}, nil
}

func (fakeRuntime) ListByLabel(context.Context, string, string) ([]string, error) { return nil, nil }

func TestSchemaCarriesTheEmbeddedSchema(t *testing.T) {
	schema := Step{}.Schema()

	assert.Contains(t, string(schema), "#Config")
	assert.Contains(t, string(schema), "workers")
}

func TestStep(t *testing.T) {
	assert.Equal(t, Step{}, New())
	assert.Equal(t, plugin.StepKindResource, Step{}.Kind())
	assert.True(t, Step{}.Idempotent(), "Up always removes a stale cluster before creating a fresh one")
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
		assert.Equal(t, kindExpose{Address: "postgres.default.svc:5432", HostPort: 15432}, cfg.Expose["postgres"])
	})

	t.Run("reads workers", func(t *testing.T) {
		cfg, err := decode([]byte(`{"workers":{"worker_a":{},"worker_b":{}}}`))
		require.NoError(t, err)

		assert.Equal(t, map[string]map[string]any{"worker_a": {}, "worker_b": {}}, cfg.Workers)
	})

	t.Run("reads control_plane and worker passthrough", func(t *testing.T) {
		cfg, err := decode([]byte(`{"control_plane":{"image":"a"},"workers":{"worker_a":{"image":"b"}}}`))
		require.NoError(t, err)

		assert.Equal(t, map[string]any{"image": "a"}, cfg.ControlPlane)
		assert.Equal(t, map[string]map[string]any{"worker_a": {"image": "b"}}, cfg.Workers)
	})
}

func TestResolvePath(t *testing.T) {
	cases := []struct {
		name, path, projectDir, want string
	}{
		{name: "empty path passes through", path: "", projectDir: "/proj", want: ""},
		{name: "absolute path is untouched", path: "/abs/path", projectDir: "/proj", want: "/abs/path"},
		{name: "no project dir passes through", path: "rel/path", projectDir: "", want: "rel/path"},
		{name: "relative path joins the project dir", path: "rel/path", projectDir: "/proj", want: filepath.Join("/proj", "rel/path")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, resolvePath(c.path, c.projectDir))
		})
	}
}

func TestResolveMountPaths(t *testing.T) {
	t.Run("nil node is a no-op", func(t *testing.T) {
		assert.NotPanics(t, func() { resolveMountPaths(nil, "/proj") })
	})

	t.Run("no extraMounts key is a no-op", func(t *testing.T) {
		node := map[string]any{"image": "kindest/node:v1.34.0"}
		resolveMountPaths(node, "/proj")
		assert.Equal(t, map[string]any{"image": "kindest/node:v1.34.0"}, node)
	})

	t.Run("an extraMounts value shaped unlike a list is a no-op", func(t *testing.T) {
		node := map[string]any{"extraMounts": "not-a-list"}
		resolveMountPaths(node, "/proj")
		assert.Equal(t, "not-a-list", node["extraMounts"])
	})

	t.Run("a mount entry shaped unlike a map is left untouched", func(t *testing.T) {
		node := map[string]any{"extraMounts": []any{"not-a-map"}}
		resolveMountPaths(node, "/proj")
		assert.Equal(t, []any{"not-a-map"}, node["extraMounts"])
	})

	t.Run("a hostPath shaped unlike a string is left untouched", func(t *testing.T) {
		mount := map[string]any{"hostPath": 5}
		node := map[string]any{"extraMounts": []any{mount}}
		resolveMountPaths(node, "/proj")
		assert.Equal(t, 5, mount["hostPath"])
	})

	t.Run("rewrites a relative hostPath against the project dir, in place", func(t *testing.T) {
		mount := map[string]any{"hostPath": "rel/path", "containerPath": "/workspace"}
		node := map[string]any{"extraMounts": []any{mount}}

		resolveMountPaths(node, "/proj")

		assert.Equal(t, filepath.Join("/proj", "rel/path"), mount["hostPath"])
		assert.Equal(t, "/workspace", mount["containerPath"], "containerPath is left untouched")
	})

	t.Run("an absolute hostPath is untouched", func(t *testing.T) {
		mount := map[string]any{"hostPath": "/abs/path"}
		node := map[string]any{"extraMounts": []any{mount}}

		resolveMountPaths(node, "/proj")

		assert.Equal(t, "/abs/path", mount["hostPath"])
	})
}

func TestWantsRelay(t *testing.T) {
	assert.False(t, wantsRelay(config{}), "no expose entries and relay unset means no relay")
	assert.True(t, wantsRelay(config{Expose: map[string]kindExpose{"a": {Address: "x:1"}}}))
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

// kindClusterYAML mirrors the shape clusterConfig generates, so a test can
// unmarshal its output back and assert on values - yaml.Marshal gives no
// guarantee over map key order, so asserting on the raw text directly would
// be asserting on an implementation detail.
type kindClusterYAML struct {
	APIVersion string         `yaml:"apiVersion"`
	Nodes      []kindNodeYAML `yaml:"nodes"`
}

type kindNodeYAML struct {
	Role              string            `yaml:"role"`
	Labels            map[string]string `yaml:"labels"`
	Image             string            `yaml:"image"`
	ExtraMounts       []kindMountYAML   `yaml:"extraMounts"`
	ExtraPortMappings []kindPortMapYAML `yaml:"extraPortMappings"`
}

type kindMountYAML struct {
	HostPath      string `yaml:"hostPath"`
	ContainerPath string `yaml:"containerPath"`
}

type kindPortMapYAML struct {
	ContainerPort int    `yaml:"containerPort"`
	HostPort      int    `yaml:"hostPort"`
	ListenAddress string `yaml:"listenAddress"`
	Protocol      string `yaml:"protocol"`
}

func parseClusterConfig(t *testing.T, text string) kindClusterYAML {
	t.Helper()
	var doc kindClusterYAML
	require.NoError(t, yaml.Unmarshal([]byte(text), &doc))
	return doc
}

func TestClusterConfig(t *testing.T) {
	t.Run("counts the nodes", func(t *testing.T) {
		tests := []struct {
			name    string
			workers map[string]map[string]any
			want    int
		}{
			{name: "control plane only", workers: nil, want: 1},
			{name: "one worker", workers: map[string]map[string]any{"worker": {}}, want: 2},
			{name: "three workers", workers: map[string]map[string]any{"a": {}, "b": {}, "c": {}}, want: 4},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := clusterConfig(config{Workers: tt.workers}, 0)
				require.NoError(t, err)

				doc := parseClusterConfig(t, got)
				assert.Equal(t, "kind.x-k8s.io/v1alpha4", doc.APIVersion)
				assert.Len(t, doc.Nodes, tt.want)

				var controlPlanes, workers int
				for _, n := range doc.Nodes {
					switch n.Role {
					case "control-plane":
						controlPlanes++
					case "worker":
						workers++
					}
				}
				assert.Equal(t, 1, controlPlanes)
				assert.Equal(t, len(tt.workers), workers)
			})
		}
	})

	t.Run("labels the control plane and each worker by name", func(t *testing.T) {
		got, err := clusterConfig(config{Workers: map[string]map[string]any{"worker_a": {}, "worker_b": {}}}, 0)
		require.NoError(t, err)

		doc := parseClusterConfig(t, got)
		names := make([]string, len(doc.Nodes))
		for i, n := range doc.Nodes {
			names[i] = n.Labels[nodeLabelKey]
		}
		assert.ElementsMatch(t, []string{"control-plane", "worker_a", "worker_b"}, names)
	})

	t.Run("prefers an explicit config", func(t *testing.T) {
		raw := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  apiServerPort: 6443\n"

		got, err := clusterConfig(config{Config: raw, Workers: map[string]map[string]any{"a": {}, "b": {}}}, 0)
		require.NoError(t, err)

		assert.Equal(t, raw, got)
		assert.NotContains(t, got, "role: worker", "workers is ignored when config is set")
	})

	t.Run("adds extra port mappings for the relay", func(t *testing.T) {
		without, err := clusterConfig(config{}, 0)
		require.NoError(t, err)
		assert.Empty(t, parseClusterConfig(t, without).Nodes[0].ExtraPortMappings, "no relay means no port mapping")

		with, err := clusterConfig(config{}, 54321)
		require.NoError(t, err)
		mappings := parseClusterConfig(t, with).Nodes[0].ExtraPortMappings
		require.Len(t, mappings, 1)
		assert.Equal(t, kindPortMapYAML{ContainerPort: 1080, HostPort: 54321, ListenAddress: "127.0.0.1", Protocol: "TCP"}, mappings[0])
	})

	t.Run("control_plane passthrough merges image and extraMounts", func(t *testing.T) {
		got, err := clusterConfig(config{ControlPlane: map[string]any{
			"image": "kindest/node:custom",
			"extraMounts": []any{
				map[string]any{"hostPath": "/src", "containerPath": "/workspace"},
			},
		}}, 0)
		require.NoError(t, err)

		node := parseClusterConfig(t, got).Nodes[0]
		assert.Equal(t, "kindest/node:custom", node.Image)
		assert.Equal(t, []kindMountYAML{{HostPath: "/src", ContainerPath: "/workspace"}}, node.ExtraMounts)
		assert.Equal(t, "control-plane", node.Labels[nodeLabelKey], "kevin's own label survives alongside the passthrough")
	})

	t.Run("a worker's own passthrough merges its own image", func(t *testing.T) {
		got, err := clusterConfig(config{Workers: map[string]map[string]any{
			"worker_a": {"image": "kindest/node:worker-only"},
		}}, 0)
		require.NoError(t, err)

		doc := parseClusterConfig(t, got)
		require.Len(t, doc.Nodes, 2)
		assert.Empty(t, doc.Nodes[0].Image, "the control plane's own image is untouched by a worker's passthrough")

		worker := doc.Nodes[1]
		assert.Equal(t, "worker", worker.Role)
		assert.Equal(t, "kindest/node:worker-only", worker.Image)
		assert.Equal(t, "worker_a", worker.Labels[nodeLabelKey])
	})

	t.Run("a passthrough labels entry merges alongside kevin's own", func(t *testing.T) {
		got, err := clusterConfig(config{ControlPlane: map[string]any{
			"labels": map[string]any{"custom-label": "yes"},
		}}, 0)
		require.NoError(t, err)

		node := parseClusterConfig(t, got).Nodes[0]
		assert.Equal(t, "control-plane", node.Labels[nodeLabelKey])
		assert.Equal(t, "yes", node.Labels["custom-label"])
	})

	t.Run("a passthrough extraPortMappings combines with the relay's own mapping", func(t *testing.T) {
		got, err := clusterConfig(config{ControlPlane: map[string]any{
			"extraPortMappings": []any{
				map[string]any{"containerPort": 8080, "hostPort": 18080, "listenAddress": "0.0.0.0", "protocol": "TCP"},
			},
		}}, 54321)
		require.NoError(t, err)

		mappings := parseClusterConfig(t, got).Nodes[0].ExtraPortMappings
		require.Len(t, mappings, 2)
		assert.Equal(t, kindPortMapYAML{ContainerPort: 1080, HostPort: 54321, ListenAddress: "127.0.0.1", Protocol: "TCP"}, mappings[0],
			"the relay's own mapping stays alongside a passthrough one, not replaced by it")
		assert.Equal(t, kindPortMapYAML{ContainerPort: 8080, HostPort: 18080, ListenAddress: "0.0.0.0", Protocol: "TCP"}, mappings[1])
	})

	t.Run("role in a passthrough errors", func(t *testing.T) {
		_, err := clusterConfig(config{ControlPlane: map[string]any{"role": "worker"}}, 0)
		require.ErrorIs(t, err, ErrReservedNodeField)

		_, err = clusterConfig(config{Workers: map[string]map[string]any{"a": {"role": "control-plane"}}}, 0)
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("a kevin.node label in a passthrough errors", func(t *testing.T) {
		_, err := clusterConfig(config{ControlPlane: map[string]any{
			"labels": map[string]any{nodeLabelKey: "not-allowed"},
		}}, 0)
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("a labels passthrough shaped unlike a map errors", func(t *testing.T) {
		_, err := clusterConfig(config{ControlPlane: map[string]any{"labels": "not-a-map"}}, 0)
		require.ErrorIs(t, err, ErrInvalidNodeField)
	})

	t.Run("an extraPortMappings passthrough shaped unlike a list errors when kevin also contributes one", func(t *testing.T) {
		_, err := clusterConfig(config{ControlPlane: map[string]any{"extraPortMappings": "not-a-list"}}, 54321)
		require.ErrorIs(t, err, ErrInvalidNodeField)
	})

	t.Run("ignores passthrough when config is set", func(t *testing.T) {
		raw := "kind: Cluster\n"
		got, err := clusterConfig(config{Config: raw, ControlPlane: map[string]any{
			"extraMounts": []any{map[string]any{"hostPath": "/src", "containerPath": "/workspace"}},
		}}, 0)
		require.NoError(t, err)
		assert.Equal(t, raw, got)
	})
}

func TestReuseFingerprint(t *testing.T) {
	cfg := config{Workers: map[string]map[string]any{"worker": {}}}

	t.Run("no proxy env yields the same fingerprint regardless of the map", func(t *testing.T) {
		without, err := reuseFingerprint(cfg, 0, nil)
		require.NoError(t, err)
		empty, err := reuseFingerprint(cfg, 0, map[string]string{})
		require.NoError(t, err)
		assert.Equal(t, without, empty)
	})

	t.Run("carries the resolved proxy endpoint", func(t *testing.T) {
		got, err := reuseFingerprint(cfg, 0, map[string]string{"HTTP_PROXY": "http://host.docker.internal:54321"})
		require.NoError(t, err)
		assert.Contains(t, got, "http://host.docker.internal:54321")
	})

	t.Run("a different proxy endpoint changes the fingerprint", func(t *testing.T) {
		a, err := reuseFingerprint(cfg, 0, map[string]string{"HTTP_PROXY": "http://host.docker.internal:1"})
		require.NoError(t, err)
		b, err := reuseFingerprint(cfg, 0, map[string]string{"HTTP_PROXY": "http://host.docker.internal:2"})
		require.NoError(t, err)
		assert.NotEqual(t, a, b, "a cluster created against one proxy address must not fingerprint as reusable against another")
	})

	t.Run("propagates a clusterConfig failure", func(t *testing.T) {
		_, err := reuseFingerprint(config{ControlPlane: map[string]any{"role": "worker"}}, 0, nil)
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("the cluster config prefix is unchanged", func(t *testing.T) {
		got, err := reuseFingerprint(cfg, 0, map[string]string{"HTTP_PROXY": "http://host.docker.internal:54321"})
		require.NoError(t, err)
		generated, err := clusterConfig(cfg, 0)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(got, generated),
			"the fingerprint must extend clusterConfig's own text, not replace it - configMarkerFile's content is never fed back into kind create")
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

func TestProviderEnv(t *testing.T) {
	t.Run("nil for docker", func(t *testing.T) {
		assert.Nil(t, providerEnv(plugin.Env{Engine: "docker"}))
		assert.Nil(t, providerEnv(plugin.Env{}))
	})

	t.Run("sets KIND_EXPERIMENTAL_PROVIDER for podman", func(t *testing.T) {
		assert.Equal(t, map[string]string{"KIND_EXPERIMENTAL_PROVIDER": "podman"}, providerEnv(plugin.Env{Engine: "podman"}))
	})
}

func TestMergeEnv(t *testing.T) {
	t.Run("either side nil returns the other unchanged", func(t *testing.T) {
		a := map[string]string{"A": "1"}
		assert.Equal(t, a, mergeEnv(a, nil))
		assert.Equal(t, a, mergeEnv(nil, a))
		assert.Nil(t, mergeEnv(nil, nil))
	})

	t.Run("combines both, b winning on a shared key", func(t *testing.T) {
		got := mergeEnv(map[string]string{"A": "1", "SHARED": "a"}, map[string]string{"B": "2", "SHARED": "b"})
		assert.Equal(t, map[string]string{"A": "1", "B": "2", "SHARED": "b"}, got)
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

func TestUpReportsABadEngine(t *testing.T) {
	_, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   "cluster",
		Config: []byte(`{}`),
		Env:    plugin.Env{Project: "demo", Workspace: t.TempDir(), Engine: "bogus"},
	}, &capture{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestFinishClusterSetup(t *testing.T) {
	nodes := []string{"demo-cluster-control-plane"}

	happyRT := fakeRuntime{
		exec: func(_ context.Context, _ string, args ...string) (string, error) {
			switch {
			case slices.Contains(args, "kubeadm-config"):
				return clusterConfigurationFixture, nil
			case slices.Contains(args, "get") && slices.Contains(args, "coredns"):
				return ".:53 {\n    forward . 8.8.8.8\n}\n", nil
			}
			return "", nil
		},
		execInput: func(context.Context, string, io.Reader, ...string) (string, error) { return "", nil },
		inspect: func(_ context.Context, name string) (cri.Container, error) {
			return cri.Container{ID: name + "-id", NetnsPath: "/proc/1/ns/net"}, nil
		},
	}

	t.Run("with every opt-out set, does nothing", func(t *testing.T) {
		exposed, containers, err := finishClusterSetup(t.Context(), fakeRuntime{}, config{},
			&plugin.UpRequest{}, "demo-cluster", nodes, "", false, &capture{})
		require.NoError(t, err)
		assert.Nil(t, exposed)
		assert.Nil(t, containers)
	})

	t.Run("propagates a trust CA failure", func(t *testing.T) {
		req := &plugin.UpRequest{Env: plugin.Env{CAPath: filepath.Join(t.TempDir(), "missing.pem")}}
		_, _, err := finishClusterSetup(t.Context(), happyRT, config{TrustCA: true}, req, "demo-cluster", nodes, "", false, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the kevin root certificate")
	})

	t.Run("propagates a coredns patch failure", func(t *testing.T) {
		req := &plugin.UpRequest{Env: plugin.Env{Domain: "kevin.home", Relay: "10.244.0.5:53"}}
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec failed")
		}}
		_, _, err := finishClusterSetup(t.Context(), rt, config{CoreDNS: true}, req, "demo-cluster", nodes, "", false, &capture{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read the coredns Corefile")
	})

	t.Run("propagates a capture discovery failure", func(t *testing.T) {
		req := &plugin.UpRequest{Env: plugin.Env{Relay: "10.244.0.5:53"}}
		_, _, err := finishClusterSetup(t.Context(), fakeRuntime{}, config{}, req, "demo-cluster",
			[]string{"demo-cluster-worker"}, "", false, &capture{})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})

	t.Run("propagates a relay failure", func(t *testing.T) {
		_, _, err := finishClusterSetup(t.Context(), fakeRuntime{}, config{}, &plugin.UpRequest{}, "demo-cluster",
			[]string{"demo-cluster-worker"}, "127.0.0.1:54321", true, &capture{})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})

	t.Run("assembles containers when capture is on and no relay is wanted", func(t *testing.T) {
		req := &plugin.UpRequest{Env: plugin.Env{Relay: "10.244.0.5:53"}}
		exposed, containers, err := finishClusterSetup(t.Context(), happyRT, config{}, req, "demo-cluster", nodes, "", false, &capture{})
		require.NoError(t, err)
		assert.Nil(t, exposed)
		require.Len(t, containers, 1)
	})
}

func TestClusterOutputs(t *testing.T) {
	got := clusterOutputs("demo-cluster", "/workspace/kubeconfig/demo-cluster", []string{"demo-cluster-control-plane", "demo-cluster-worker"})

	assert.Equal(t, map[string]string{
		"name":       "demo-cluster",
		"kubeconfig": "/workspace/kubeconfig/demo-cluster",
		"context":    "kind-demo-cluster",
		"nodes":      "demo-cluster-control-plane,demo-cluster-worker",
	}, got)
}

func TestExport(t *testing.T) {
	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := Step{}.Export(t.Context(), &plugin.ExportRequest{Config: []byte(`{`)})
		require.Error(t, err)
	})

	t.Run("no kubeconfig yet is an error", func(t *testing.T) {
		_, err := Step{}.Export(t.Context(), &plugin.ExportRequest{
			Env: plugin.Env{Project: "demo", Workspace: t.TempDir()},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "run `kevin run` or `kevin setup` first")
	})

	t.Run("reports the persisted values with no live containers when the engine can't be reached", func(t *testing.T) {
		workspace := t.TempDir()
		name := "demo-cluster"
		kubeconfig := filepath.Join(workspace, "kubeconfig", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(kubeconfig), 0o700))
		require.NoError(t, os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600))

		got, err := Step{}.Export(t.Context(), &plugin.ExportRequest{
			Step: "cluster",
			Env:  plugin.Env{Project: "demo", Workspace: workspace, Engine: "bogus"},
		})
		require.NoError(t, err)
		assert.Equal(t, plugin.StringMap(map[string]string{
			"name":       name,
			"kubeconfig": kubeconfig,
			"context":    "kind-" + name,
		}), got.Out)
		assert.Nil(t, got.Containers)
	})

	t.Run("reads the relay address back when Up left one", func(t *testing.T) {
		workspace := t.TempDir()
		name := "demo-cluster"
		kubeconfig := filepath.Join(workspace, "kubeconfig", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(kubeconfig), 0o700))
		require.NoError(t, os.WriteFile(kubeconfig, []byte("apiVersion: v1\n"), 0o600))
		require.NoError(t, os.WriteFile(relayAddrFile(kubeconfig), []byte("127.0.0.1:54321"), 0o600))

		got, err := Step{}.Export(t.Context(), &plugin.ExportRequest{
			Step: "cluster",
			Env:  plugin.Env{Project: "demo", Workspace: workspace, Engine: "bogus"},
		})
		require.NoError(t, err)
		assert.Equal(t, "127.0.0.1:54321", got.Out["relay_addr"].Reveal())
	})
}

func TestDownReportsBadConfig(t *testing.T) {
	err := Step{}.Down(t.Context(), &plugin.DownRequest{Config: []byte(`{`)}, &capture{})
	require.Error(t, err)
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

func TestExportContainers(t *testing.T) {
	t.Run("an unsupported engine fails open", func(t *testing.T) {
		containers, err := exportContainers(t.Context(), &plugin.ExportRequest{
			Env: plugin.Env{Engine: "bogus"},
		}, "kevin-kind-absent-cluster")
		require.NoError(t, err)
		assert.Nil(t, containers)
	})

	t.Run("a cluster that no longer exists fails open", func(t *testing.T) {
		requireDocker(t)
		requireKind(t)

		containers, err := exportContainers(t.Context(), &plugin.ExportRequest{
			Env: plugin.Env{},
		}, "kevin-kind-absent-cluster")
		require.NoError(t, err)
		assert.Nil(t, containers)
	})
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

func TestPickHostPort(t *testing.T) {
	port, err := findFreePort(t.Context())
	require.NoError(t, err)
	assert.Positive(t, port)
}

func TestExposedViaRelay(t *testing.T) {
	t.Run("builds a socks5 upstream per entry, sorted by name", func(t *testing.T) {
		got := exposedViaRelay(map[string]kindExpose{
			"postgres":   {Address: "postgres.default.svc:5432"},
			"kubernetes": {Address: "kubernetes.default.svc:443"},
		}, "127.0.0.1:54321")

		require.Len(t, got, 2)
		assert.Equal(t, plugin.ExposedPort{
			Name: "kubernetes", Protocol: "socks5",
			Upstream: "socks5://127.0.0.1:54321/kubernetes.default.svc:443",
		}, got[0])
		assert.Equal(t, plugin.ExposedPort{
			Name: "postgres", Protocol: "socks5",
			Upstream: "socks5://127.0.0.1:54321/postgres.default.svc:5432",
		}, got[1])
	})

	t.Run("entries convert to card details", func(t *testing.T) {
		got := exposedViaRelay(map[string]kindExpose{"postgres": {Address: "postgres.default.svc:5432"}}, "127.0.0.1:54321")

		require.Len(t, got, 1)
		assert.Equal(t, plugin.Detail{
			Label: "socks5 postgres", Value: plugin.String("socks5://127.0.0.1:54321/postgres.default.svc:5432"), Copyable: true,
		}, got[0].Detail(), "Up must mirror every exposed port onto the card the same way")
	})

	t.Run("threads host_port through to the exposed port", func(t *testing.T) {
		got := exposedViaRelay(map[string]kindExpose{
			"postgres": {Address: "postgres.default.svc:5432", HostPort: 15432},
		}, "127.0.0.1:54321")

		require.Len(t, got, 1)
		assert.Equal(t, 15432, got[0].HostPort)
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
	t.Run("no control-plane node is a hard failure", func(t *testing.T) {
		err := deployRelay(t.Context(), fakeRuntime{}, "demo-cluster", []string{"demo-cluster-worker"},
			"kevin-relay:dev", plugin.Env{}, &capture{})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})

	t.Run("saving the relay image fails before kind ever loads it", func(t *testing.T) {
		rt := fakeRuntime{save: func(context.Context, string) (io.ReadCloser, error) {
			return nil, errors.New("no such image")
		}}

		err := deployRelay(t.Context(), rt, "demo-cluster", []string{"demo-cluster-control-plane"},
			"kevin-relay:dev", plugin.Env{}, &capture{})
		require.Error(t, err)
	})
}

func TestFinishRelay(t *testing.T) {
	t.Run("propagates a deployRelay failure instead of reporting exposed ports", func(t *testing.T) {
		_, err := finishRelay(t.Context(), fakeRuntime{}, config{}, "demo-cluster", []string{"demo-cluster-worker"},
			"127.0.0.1:54321", plugin.Env{}, &capture{})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})
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
