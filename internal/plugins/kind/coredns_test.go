package kind

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestPointNodeDNSAtRelay(t *testing.T) {
	t.Run("rewrites resolv.conf on every node", func(t *testing.T) {
		var got []string
		rt := fakeRuntime{exec: func(_ context.Context, container string, args ...string) (string, error) {
			got = append(got, container)
			assert.Equal(t, []string{"sh", "-c", "echo nameserver 10.244.0.5 > /etc/resolv.conf"}, args)
			return "", nil
		}}

		err := pointNodeDNSAtRelay(t.Context(), rt, []string{"demo-control-plane", "demo-worker"}, "10.244.0.5")
		require.NoError(t, err)
		assert.Equal(t, []string{"demo-control-plane", "demo-worker"}, got)
	})

	t.Run("one node failing stops at that node", func(t *testing.T) {
		var got []string
		rt := fakeRuntime{exec: func(_ context.Context, container string, _ ...string) (string, error) {
			got = append(got, container)
			if container == "demo-worker" {
				return "", errors.New("exec: no such container")
			}
			return "", nil
		}}

		err := pointNodeDNSAtRelay(t.Context(), rt, []string{"demo-worker", "demo-control-plane"}, "10.244.0.5")
		require.Error(t, err)
		assert.Equal(t, []string{"demo-worker"}, got, "a node after the failure is never reached")
	})
}

// patchCoreDNSRuntime builds a fakeRuntime that walks patchCoreDNS's call
// sequence (get Corefile, render configmap, replace it, point every node's
// DNS at the relay, restart coredns, wait for the rollout) and fails on the
// callN'th call, or never when callN is 0.
func patchCoreDNSRuntime(callN int) fakeRuntime {
	call := 0
	fails := func() bool {
		call++
		return call == callN
	}
	return fakeRuntime{
		exec: func(_ context.Context, _ string, args ...string) (string, error) {
			if fails() {
				return "", errors.New("exec failed")
			}
			if slices.Contains(args, "get") {
				return ".:53 {\n    forward . 8.8.8.8\n}\n", nil
			}
			return "", nil
		},
		execInput: func(_ context.Context, _ string, _ io.Reader, _ ...string) (string, error) {
			if fails() {
				return "", errors.New("exec failed")
			}
			return "", nil
		},
	}
}

func TestPatchCoreDNS(t *testing.T) {
	nodes := []string{"demo-cluster-control-plane"}

	t.Run("happy path patches the Corefile, DNS, and restarts coredns", func(t *testing.T) {
		out := &capture{}
		err := patchCoreDNS(t.Context(), patchCoreDNSRuntime(0), nodes, "kevin.home", "10.244.0.5", out)
		require.NoError(t, err)
		assert.Contains(t, strings.Join(out.stdout, "\n"), "coredns resolves *.kevin.home")
	})

	t.Run("no control-plane node is a hard failure", func(t *testing.T) {
		err := patchCoreDNS(t.Context(), patchCoreDNSRuntime(0), []string{"demo-cluster-worker"}, "kevin.home", "10.244.0.5", &capture{})
		require.ErrorIs(t, err, ErrNoControlPlaneNode)
	})

	for i, step := range []string{
		"read the coredns Corefile",
		"render the coredns configmap",
		"replace the coredns configmap",
		"point demo-cluster-control-plane's dns at the relay",
		"restart coredns",
		"wait for coredns",
	} {
		t.Run(step+" failing propagates", func(t *testing.T) {
			err := patchCoreDNS(t.Context(), patchCoreDNSRuntime(i+1), nodes, "kevin.home", "10.244.0.5", &capture{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), step)
		})
	}
}
