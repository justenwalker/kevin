package kind

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestNetnsTargets(t *testing.T) {
	t.Run("no control-plane node is a hard failure", func(t *testing.T) {
		_, err := netnsTargets(t.Context(), "cluster", []string{"kevin-demo-worker"}, &capture{})
		assert.ErrorIs(t, err, ErrNoControlPlaneNode)
	})
}
