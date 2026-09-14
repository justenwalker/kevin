package kind

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

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
