package kind

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
