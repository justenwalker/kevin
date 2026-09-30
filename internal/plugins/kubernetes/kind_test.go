package kubernetes

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/plugin"
)

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
	ReadOnly      bool   `yaml:"readOnly"`
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
				got, err := clusterConfig(config{Workers: tt.workers}, clusterrelay.Ports{})
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
		got, err := clusterConfig(config{Workers: map[string]map[string]any{"worker_a": {}, "worker_b": {}}}, clusterrelay.Ports{})
		require.NoError(t, err)

		doc := parseClusterConfig(t, got)
		names := make([]string, len(doc.Nodes))
		for i, n := range doc.Nodes {
			names[i] = n.Labels[nodeLabelKey]
		}
		assert.ElementsMatch(t, []string{"control-plane", "worker_a", "worker_b"}, names)
	})

	t.Run("mounts reach every node", func(t *testing.T) {
		cfg := config{
			Workers: map[string]map[string]any{"a": {}, "b": {}},
			Mounts:  []mount{{Host: "/src", Container: "/workspace"}, {Host: "/data", Container: "/data", ReadOnly: true}},
		}

		text, err := clusterConfig(cfg, clusterrelay.Ports{})
		require.NoError(t, err)

		doc := parseClusterConfig(t, text)
		require.Len(t, doc.Nodes, 3)
		for _, node := range doc.Nodes {
			assert.Equal(t, []kindMountYAML{
				{HostPath: "/src", ContainerPath: "/workspace"},
				{HostPath: "/data", ContainerPath: "/data", ReadOnly: true},
			}, node.ExtraMounts, "node %s", node.Role)
		}
	})

	t.Run("mounts come before the extraMounts of a node config", func(t *testing.T) {
		cfg := config{
			Mounts: []mount{{Host: "/src", Container: "/workspace"}},
			Kind: kindConfig{ControlPlane: map[string]any{
				"extraMounts": []any{map[string]any{"hostPath": "/extra", "containerPath": "/extra"}},
			}},
		}

		text, err := clusterConfig(cfg, clusterrelay.Ports{})
		require.NoError(t, err)

		doc := parseClusterConfig(t, text)
		assert.Equal(t, []kindMountYAML{
			{HostPath: "/src", ContainerPath: "/workspace"},
			{HostPath: "/extra", ContainerPath: "/extra"},
		}, doc.Nodes[0].ExtraMounts)
	})

	t.Run("an extraMounts value shaped unlike a list is an error", func(t *testing.T) {
		cfg := config{
			Mounts: []mount{{Host: "/src", Container: "/workspace"}},
			Kind:   kindConfig{ControlPlane: map[string]any{"extraMounts": "not-a-list"}},
		}

		_, err := clusterConfig(cfg, clusterrelay.Ports{})
		require.ErrorIs(t, err, ErrInvalidNodeField)
	})

	t.Run("prefers an explicit config", func(t *testing.T) {
		raw := "kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  apiServerPort: 6443\n"

		got, err := clusterConfig(config{Kind: kindConfig{Config: raw}, Workers: map[string]map[string]any{"a": {}, "b": {}}}, clusterrelay.Ports{})
		require.NoError(t, err)

		assert.Equal(t, raw, got)
		assert.NotContains(t, got, "role: worker", "workers is ignored when config is set")
	})

	t.Run("adds extra port mappings for the relay", func(t *testing.T) {
		without, err := clusterConfig(config{}, clusterrelay.Ports{})
		require.NoError(t, err)
		assert.Empty(t, parseClusterConfig(t, without).Nodes[0].ExtraPortMappings, "no relay means no port mapping")

		with, err := clusterConfig(config{}, clusterrelay.Ports{TCP: 54321})
		require.NoError(t, err)
		mappings := parseClusterConfig(t, with).Nodes[0].ExtraPortMappings
		require.Len(t, mappings, 1)
		assert.Equal(t, kindPortMapYAML{ContainerPort: 1080, HostPort: 54321, ListenAddress: "127.0.0.1", Protocol: "TCP"}, mappings[0])
	})

	t.Run("control_plane passthrough merges image and extraMounts", func(t *testing.T) {
		got, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{
			"image": "kindest/node:custom",
			"extraMounts": []any{
				map[string]any{"hostPath": "/src", "containerPath": "/workspace"},
			},
		}}}, clusterrelay.Ports{})
		require.NoError(t, err)

		node := parseClusterConfig(t, got).Nodes[0]
		assert.Equal(t, "kindest/node:custom", node.Image)
		assert.Equal(t, []kindMountYAML{{HostPath: "/src", ContainerPath: "/workspace"}}, node.ExtraMounts)
		assert.Equal(t, "control-plane", node.Labels[nodeLabelKey], "kevin's own label survives alongside the passthrough")
	})

	t.Run("a worker's own passthrough merges its own image", func(t *testing.T) {
		got, err := clusterConfig(config{Workers: map[string]map[string]any{
			"worker_a": {"image": "kindest/node:worker-only"},
		}}, clusterrelay.Ports{})
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
		got, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{
			"labels": map[string]any{"custom-label": "yes"},
		}}}, clusterrelay.Ports{})
		require.NoError(t, err)

		node := parseClusterConfig(t, got).Nodes[0]
		assert.Equal(t, "control-plane", node.Labels[nodeLabelKey])
		assert.Equal(t, "yes", node.Labels["custom-label"])
	})

	t.Run("a passthrough extraPortMappings combines with the relay's own mapping", func(t *testing.T) {
		got, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{
			"extraPortMappings": []any{
				map[string]any{"containerPort": 8080, "hostPort": 18080, "listenAddress": "0.0.0.0", "protocol": "TCP"},
			},
		}}}, clusterrelay.Ports{TCP: 54321})
		require.NoError(t, err)

		mappings := parseClusterConfig(t, got).Nodes[0].ExtraPortMappings
		require.Len(t, mappings, 2)
		assert.Equal(t, kindPortMapYAML{ContainerPort: 1080, HostPort: 54321, ListenAddress: "127.0.0.1", Protocol: "TCP"}, mappings[0],
			"the relay's own mapping stays alongside a passthrough one, not replaced by it")
		assert.Equal(t, kindPortMapYAML{ContainerPort: 8080, HostPort: 18080, ListenAddress: "0.0.0.0", Protocol: "TCP"}, mappings[1])
	})

	t.Run("role in a passthrough errors", func(t *testing.T) {
		_, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{"role": "worker"}}}, clusterrelay.Ports{})
		require.ErrorIs(t, err, ErrReservedNodeField)

		_, err = clusterConfig(config{Workers: map[string]map[string]any{"a": {"role": "control-plane"}}}, clusterrelay.Ports{})
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("a kevin.node label in a passthrough errors", func(t *testing.T) {
		_, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{
			"labels": map[string]any{nodeLabelKey: "not-allowed"},
		}}}, clusterrelay.Ports{})
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("a labels passthrough shaped unlike a map errors", func(t *testing.T) {
		_, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{"labels": "not-a-map"}}}, clusterrelay.Ports{})
		require.ErrorIs(t, err, ErrInvalidNodeField)
	})

	t.Run("an extraPortMappings passthrough shaped unlike a list errors when kevin also contributes one", func(t *testing.T) {
		_, err := clusterConfig(config{Kind: kindConfig{ControlPlane: map[string]any{"extraPortMappings": "not-a-list"}}}, clusterrelay.Ports{TCP: 54321})
		require.ErrorIs(t, err, ErrInvalidNodeField)
	})

	t.Run("ignores passthrough when config is set", func(t *testing.T) {
		raw := "kind: Cluster\n"
		got, err := clusterConfig(config{Kind: kindConfig{Config: raw, ControlPlane: map[string]any{
			"extraMounts": []any{map[string]any{"hostPath": "/src", "containerPath": "/workspace"}},
		}}}, clusterrelay.Ports{})
		require.NoError(t, err)
		assert.Equal(t, raw, got)
	})
}

func TestKindDriverFingerprint(t *testing.T) {
	cfg := config{Workers: map[string]map[string]any{"worker": {}}}

	reuseFingerprint := func(cfg config, ports clusterrelay.Ports, proxy map[string]string) (string, error) {
		cfg.Proxy = true
		return (&kindDriver{cfg: cfg, env: plugin.Env{ProxyEnv: proxy}}).Fingerprint(createSpec{Ports: ports})
	}

	t.Run("no proxy env yields the same fingerprint regardless of the map", func(t *testing.T) {
		without, err := reuseFingerprint(cfg, clusterrelay.Ports{}, nil)
		require.NoError(t, err)
		empty, err := reuseFingerprint(cfg, clusterrelay.Ports{}, map[string]string{})
		require.NoError(t, err)
		assert.Equal(t, without, empty)
	})

	t.Run("carries the resolved proxy endpoint", func(t *testing.T) {
		got, err := reuseFingerprint(cfg, clusterrelay.Ports{}, map[string]string{"HTTP_PROXY": "http://host.docker.internal:54321"})
		require.NoError(t, err)
		assert.Contains(t, got, "http://host.docker.internal:54321")
	})

	t.Run("a different proxy endpoint changes the fingerprint", func(t *testing.T) {
		a, err := reuseFingerprint(cfg, clusterrelay.Ports{}, map[string]string{"HTTP_PROXY": "http://host.docker.internal:1"})
		require.NoError(t, err)
		b, err := reuseFingerprint(cfg, clusterrelay.Ports{}, map[string]string{"HTTP_PROXY": "http://host.docker.internal:2"})
		require.NoError(t, err)
		assert.NotEqual(t, a, b, "a cluster created against one proxy address must not fingerprint as reusable against another")
	})

	t.Run("propagates a clusterConfig failure", func(t *testing.T) {
		_, err := reuseFingerprint(config{Kind: kindConfig{ControlPlane: map[string]any{"role": "worker"}}}, clusterrelay.Ports{}, nil)
		require.ErrorIs(t, err, ErrReservedNodeField)
	})

	t.Run("the cluster config prefix is unchanged", func(t *testing.T) {
		got, err := reuseFingerprint(cfg, clusterrelay.Ports{}, map[string]string{"HTTP_PROXY": "http://host.docker.internal:54321"})
		require.NoError(t, err)
		generated, err := clusterConfig(cfg, clusterrelay.Ports{})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(got, generated),
			"the fingerprint must extend clusterConfig's own text, not replace it - configMarkerFile's content is never fed back into kind create")
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

func TestKubectlArgs(t *testing.T) {
	t.Run("prepends the admin kubeconfig to every call", func(t *testing.T) {
		got := kubectlArgs([]string{"-n", "kube-system", "get", "configmap", "coredns"})

		assert.Equal(t, []string{
			"kubectl", "--kubeconfig", adminKubeconfig,
			"-n", "kube-system", "get", "configmap", "coredns",
		}, got, "every call must carry the admin kubeconfig of the node")
	})

	t.Run("with no args", func(t *testing.T) {
		assert.Equal(t, []string{"kubectl", "--kubeconfig", adminKubeconfig}, kubectlArgs(nil))
	})
}

func TestKindDriverNames(t *testing.T) {
	d := &kindDriver{name: "demo-cluster"}

	assert.Equal(t, "kind-demo-cluster", d.Context())
	assert.Equal(t, "demo-cluster-control-plane", d.ControlPlane(),
		"kind names the first control-plane node after the cluster, also for a hand-written HA config")
	require.NoError(t, d.LabelNodes(t.Context()), "kind labels at creation, so there is nothing to do")
}

func TestKindDriverKubectl(t *testing.T) {
	t.Run("runs kubectl in the control-plane node", func(t *testing.T) {
		var gotContainer string
		var gotArgs []string
		rt := fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			gotContainer, gotArgs = container, args
			return "ok", nil
		}}

		got, err := (&kindDriver{name: "demo-cluster", rt: rt}).Kubectl(t.Context(), "get", "nodes")
		require.NoError(t, err)
		assert.Equal(t, "ok", got)
		assert.Equal(t, "demo-cluster-control-plane", gotContainer)
		assert.Equal(t, []string{"kubectl", "--kubeconfig", adminKubeconfig, "get", "nodes"}, gotArgs)
	})

	t.Run("feeds stdin to kubectl", func(t *testing.T) {
		var gotContainer, gotStdin string
		rt := fakeRuntime{execInput: func(_ context.Context, container string, stdin io.Reader, _ ...string) (string, error) {
			b, err := io.ReadAll(stdin)
			gotContainer, gotStdin = container, string(b)
			return "", err
		}}

		_, err := (&kindDriver{name: "demo-cluster", rt: rt}).KubectlInput(t.Context(), strings.NewReader("manifest"), "apply", "-f", "-")
		require.NoError(t, err)
		assert.Equal(t, "demo-cluster-control-plane", gotContainer)
		assert.Equal(t, "manifest", gotStdin)
	})

	t.Run("no runtime is an error, not a panic", func(t *testing.T) {
		_, err := (&kindDriver{name: "demo-cluster"}).Kubectl(t.Context(), "get", "nodes")
		require.ErrorIs(t, err, ErrNoRuntime)

		_, err = (&kindDriver{name: "demo-cluster"}).KubectlInput(t.Context(), strings.NewReader(""), "apply")
		require.ErrorIs(t, err, ErrNoRuntime)
	})

	t.Run("a failing exec is an error", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: no such container")
		}}

		_, err := (&kindDriver{name: "demo-cluster", rt: rt}).Kubectl(t.Context(), "get", "nodes")
		require.Error(t, err)
	})
}

func TestNewKindDriver(t *testing.T) {
	t.Run("resolves relative mount paths against the project directory", func(t *testing.T) {
		mount := map[string]any{"hostPath": "rel/path"}
		cfg := config{Kind: kindConfig{ControlPlane: map[string]any{"extraMounts": []any{mount}}}}

		newKindDriver(cfg, plugin.Env{ProjectDir: "/proj"}, "demo-cluster", "/kubeconfig", nil)

		assert.Equal(t, filepath.Join("/proj", "rel/path"), mount["hostPath"])
	})

	t.Run("resolves worker mount paths too", func(t *testing.T) {
		mount := map[string]any{"hostPath": "rel/path"}
		cfg := config{Workers: map[string]map[string]any{"a": {"extraMounts": []any{mount}}}}

		newKindDriver(cfg, plugin.Env{ProjectDir: "/proj"}, "demo-cluster", "/kubeconfig", nil)

		assert.Equal(t, filepath.Join("/proj", "rel/path"), mount["hostPath"])
	})
}

func TestKindDriverRefreshAccess(t *testing.T) {
	require.NoError(t, (&kindDriver{name: "demo-cluster"}).RefreshAccess(t.Context()),
		"kind publishes the API server on a host port that joining a network does not change")
}
