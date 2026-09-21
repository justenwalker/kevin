package kind

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

func TestWantsCapture(t *testing.T) {
	assert.True(t, wantsCapture(plugin.Env{Relay: "10.244.0.5:53"}))
	assert.False(t, wantsCapture(plugin.Env{}), "no relay means nothing for the nodes to route through")
}

// clusterConfigurationFixture is a real kubeadm ClusterConfiguration, the
// shape kubeadm-config's own configmap carries under
// data.ClusterConfiguration.
const clusterConfigurationFixture = `apiServer: {}
apiVersion: kubeadm.k8s.io/v1beta4
kind: ClusterConfiguration
kubernetesVersion: v1.31.0
networking:
  dnsDomain: cluster.local
  podSubnet: 10.244.0.0/16
  serviceSubnet: 10.96.0.0/12
`

func TestClusterConfigValue(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want []string
	}{
		{name: "pod subnet", key: "podSubnet", want: []string{"10.244.0.0/16"}},
		{name: "service subnet", key: "serviceSubnet", want: []string{"10.96.0.0/12"}},
		{name: "a key that isn't present", key: "clusterCIDR", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterConfigValue(clusterConfigurationFixture, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("comma-separated for a dual-stack cluster", func(t *testing.T) {
		text := "networking:\n  podSubnet: 10.244.0.0/16,fd00:10:244::/56\n"
		got := clusterConfigValue(text, "podSubnet")
		assert.Equal(t, []string{"10.244.0.0/16", "fd00:10:244::/56"}, got)
	})

	t.Run("a value-less key is skipped", func(t *testing.T) {
		text := "networking:\n  podSubnet:\n  serviceSubnet: 10.96.0.0/12\n"
		assert.Empty(t, clusterConfigValue(text, "podSubnet"))
		assert.Equal(t, []string{"10.96.0.0/12"}, clusterConfigValue(text, "serviceSubnet"))
	})
}

func TestNodeContainers(t *testing.T) {
	t.Run("no control-plane node is a hard failure", func(t *testing.T) {
		_, err := nodeContainers(t.Context(), dockerClient, []string{"kevin-demo-worker"}, &capture{})
		assert.ErrorIs(t, err, ErrNoControlPlaneNode)
	})

	t.Run("assembles container info for every ready node", func(t *testing.T) {
		rt := fakeRuntime{
			exec: func(_ context.Context, _ string, args ...string) (string, error) {
				switch {
				case slices.Contains(args, "kubeadm-config"):
					return clusterConfigurationFixture, nil
				case slices.Contains(args, "nodes"):
					return nodesJSONFixture, nil
				}
				return "", nil
			},
			inspect: func(_ context.Context, name string) (cri.Container, error) {
				return cri.Container{ID: name + "-id", NetnsPath: "/proc/1/ns/net"}, nil
			},
		}

		got, err := nodeContainers(t.Context(), rt, []string{"demo-cluster-control-plane", "demo-cluster-worker"}, &capture{})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/12"}, got[0].ExcludeCIDRs)
		assert.Equal(t, "control-plane", got[0].Name, "the friendly kevin.node label name wins over the raw container name")
		assert.Equal(t, "worker_a", got[1].Name)
	})

	t.Run("CIDR discovery failing skips capture instead of failing Up", func(t *testing.T) {
		out := &capture{}
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: no such container")
		}}

		got, err := nodeContainers(t.Context(), rt, []string{"demo-cluster-control-plane"}, out)
		require.NoError(t, err)
		assert.Nil(t, got)
		assert.Contains(t, strings.Join(out.stdout, "\n"), "skipping egress capture")
	})

	t.Run("a node with no network namespace is excluded", func(t *testing.T) {
		rt := fakeRuntime{
			exec: func(_ context.Context, _ string, args ...string) (string, error) {
				if slices.Contains(args, "kubeadm-config") {
					return clusterConfigurationFixture, nil
				}
				return "", nil
			},
			inspect: func(_ context.Context, name string) (cri.Container, error) {
				if name == "demo-cluster-worker" {
					return cri.Container{ID: "worker-id"}, nil
				}
				return cri.Container{ID: "control-plane-id", NetnsPath: "/proc/1/ns/net"}, nil
			},
		}

		got, err := nodeContainers(t.Context(), rt, []string{"demo-cluster-control-plane", "demo-cluster-worker"}, &capture{})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "control-plane-id", got[0].ID)
	})

	t.Run("an Inspect failure is a hard error", func(t *testing.T) {
		rt := fakeRuntime{
			exec: func(_ context.Context, _ string, args ...string) (string, error) {
				if slices.Contains(args, "kubeadm-config") {
					return clusterConfigurationFixture, nil
				}
				return "", nil
			},
			inspect: func(context.Context, string) (cri.Container, error) {
				return cri.Container{}, errors.New("no such container")
			},
		}

		_, err := nodeContainers(t.Context(), rt, []string{"demo-cluster-control-plane"}, &capture{})
		require.Error(t, err)
	})
}

// nodesJSONFixture is a trimmed "kubectl get nodes -o json" response: one
// control-plane node with the kevin.node label, one worker node without
// it (an older cluster, created before this label existed, say).
const nodesJSONFixture = `{
	"items": [
		{"metadata": {"name": "demo-cluster-control-plane", "labels": {"kevin.node": "control-plane"}}},
		{"metadata": {"name": "demo-cluster-worker", "labels": {"kevin.node": "worker_a"}}},
		{"metadata": {"name": "demo-cluster-worker2", "labels": {}}}
	]
}`

func TestContainerInfoFor(t *testing.T) {
	info := cri.Container{ID: "abc123", NetnsPath: "/proc/1/ns/net"}
	exclude := []string{"10.244.0.0/16"}

	t.Run("substitutes the friendly name when one was read back", func(t *testing.T) {
		names := map[string]string{"demo-cluster-worker": "worker_a"}
		got := containerInfoFor("demo-cluster-worker", info, exclude, names)
		assert.Equal(t, plugin.ContainerInfo{
			ID: "abc123", Name: "worker_a", NetnsPath: "/proc/1/ns/net", ExcludeCIDRs: exclude,
		}, got)
	})

	t.Run("falls back to the raw container name otherwise", func(t *testing.T) {
		got := containerInfoFor("demo-cluster-worker2", info, exclude, nil)
		assert.Equal(t, "demo-cluster-worker2", got.Name)
	})
}

func TestPodAndServiceCIDRs(t *testing.T) {
	t.Run("parses the pod and service subnets off kubeadm-config", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return clusterConfigurationFixture, nil
		}}

		got, err := podAndServiceCIDRs(t.Context(), rt, "demo-cluster-control-plane")
		require.NoError(t, err)
		assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/12"}, got)
	})

	t.Run("kubectl failing is an error", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: no such container")
		}}

		_, err := podAndServiceCIDRs(t.Context(), rt, "demo-cluster-control-plane")
		require.Error(t, err)
	})

	t.Run("neither subnet present is ErrNoClusterCIDRs", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "networking:\n  dnsDomain: cluster.local\n", nil
		}}

		_, err := podAndServiceCIDRs(t.Context(), rt, "demo-cluster-control-plane")
		require.ErrorIs(t, err, ErrNoClusterCIDRs)
	})

	t.Run("a subnet that doesn't parse as a CIDR is an error", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "networking:\n  podSubnet: not-a-cidr\n  serviceSubnet: 10.96.0.0/12\n", nil
		}}

		_, err := podAndServiceCIDRs(t.Context(), rt, "demo-cluster-control-plane")
		require.Error(t, err)
	})
}

func TestNodeNames(t *testing.T) {
	t.Run("reads the kevin.node label back off every node", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return nodesJSONFixture, nil
		}}

		got := nodeNames(t.Context(), rt, "demo-cluster-control-plane")
		assert.Equal(t, map[string]string{
			"demo-cluster-control-plane": "control-plane",
			"demo-cluster-worker":        "worker_a",
		}, got)
	})

	t.Run("kubectl failing falls back to no names, not an error", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("exec: no such container")
		}}

		assert.Nil(t, nodeNames(t.Context(), rt, "demo-cluster-control-plane"))
	})

	t.Run("unparsable output falls back to no names, not an error", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "{", nil
		}}

		assert.Nil(t, nodeNames(t.Context(), rt, "demo-cluster-control-plane"))
	})
}

func TestParseNodeLabels(t *testing.T) {
	t.Run("keys the label value by the node's own name", func(t *testing.T) {
		names, err := parseNodeLabels(nodesJSONFixture)
		require.NoError(t, err)
		assert.Equal(t, map[string]string{
			"demo-cluster-control-plane": "control-plane",
			"demo-cluster-worker":        "worker_a",
		}, names)
	})

	t.Run("a node with no kevin.node label is simply absent", func(t *testing.T) {
		names, err := parseNodeLabels(nodesJSONFixture)
		require.NoError(t, err)
		_, ok := names["demo-cluster-worker2"]
		assert.False(t, ok)
	})

	t.Run("reports broken JSON", func(t *testing.T) {
		_, err := parseNodeLabels("{")
		assert.Error(t, err)
	})
}
