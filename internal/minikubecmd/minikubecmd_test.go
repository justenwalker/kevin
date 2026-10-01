package minikubecmd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/uerr"
)

func TestNotInstalled(t *testing.T) {
	t.Run("attaches a message when the binary is missing", func(t *testing.T) {
		err := fmt.Errorf("minikube version: %w", exec.ErrNotFound)
		got := notInstalled(err)
		require.ErrorIs(t, got, err)
		assert.Equal(t, "minikube isn't installed, or isn't on PATH - install it: https://minikube.sigs.k8s.io/docs/start/",
			uerr.Display(got))
	})

	t.Run("leaves any other failure alone", func(t *testing.T) {
		err := errors.New("minikube version: exit status 1")
		assert.Same(t, err, notInstalled(err))
	})
}

func TestEnv(t *testing.T) {
	t.Run("strips every proxy variable in both cases", func(t *testing.T) {
		for _, key := range proxyVariables {
			t.Setenv(key, "http://proxy.example:3128")
		}
		t.Setenv("MINIKUBECMD_TEST_KEEP", "yes")

		got := Env("/ws/.minikube", "")

		for _, key := range proxyVariables {
			for _, entry := range got {
				assert.False(t, strings.HasPrefix(entry, key+"="), "%s must not reach minikube", key)
			}
		}
		assert.Contains(t, got, "MINIKUBECMD_TEST_KEEP=yes")
	})

	t.Run("sets the variables minikube needs", func(t *testing.T) {
		t.Setenv("MINIKUBE_HOME", "/elsewhere")
		t.Setenv("KUBECONFIG", "/home/me/.kube/config")

		got := Env("/ws/.minikube", "/ws/kubeconfig")

		assert.Contains(t, got, "MINIKUBE_HOME=/ws/.minikube")
		assert.Contains(t, got, "KUBECONFIG=/ws/kubeconfig")
		assert.Contains(t, got, "MINIKUBE_IN_STYLE=false")
		assert.Contains(t, got, "MINIKUBE_WANTUPDATENOTIFICATION=false")
		assert.NotContains(t, got, "MINIKUBE_HOME=/elsewhere")
		assert.NotContains(t, got, "KUBECONFIG=/home/me/.kube/config")
	})

	t.Run("no kubeconfig leaves the process's own", func(t *testing.T) {
		t.Setenv("KUBECONFIG", "/home/me/.kube/config")

		assert.Contains(t, Env("/ws/.minikube", ""), "KUBECONFIG=/home/me/.kube/config")
	})
}

func TestStartArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		args := StartArgs(StartSpec{Name: "demo", Driver: "docker", Nodes: 1})
		want := []string{
			"start", "-p", "demo",
			"--driver=docker",
			"--container-runtime=containerd",
			"--nodes=1",
			"--wait=all",
			"--embed-certs",
			"--interactive=false",
		}
		assert.Equal(t, want, args)
	})

	t.Run("full", func(t *testing.T) {
		args := StartArgs(StartSpec{
			Name:              "demo",
			Driver:            "podman",
			Network:           "demo-net",
			Nodes:             3,
			Wait:              5 * time.Minute,
			KubernetesVersion: "v1.33.1",
			BaseImage:         "gcr.io/k8s-minikube/kicbase:v0.0.47",
			Memory:            "2g",
			CPUs:              2,
			Mount:             "/src:/mnt/src:ro",
			Home:              "/ws/.minikube",
			Kubeconfig:        "/ws/kubeconfig",
		})
		want := []string{
			"start", "-p", "demo",
			"--driver=podman",
			"--container-runtime=containerd",
			"--nodes=3",
			"--wait=all",
			"--embed-certs",
			"--interactive=false",
			"--wait-timeout=5m0s",
			"--network", "demo-net",
			"--kubernetes-version", "v1.33.1",
			"--base-image", "gcr.io/k8s-minikube/kicbase:v0.0.47",
			"--memory", "2g",
			"--cpus", "2",
			"--mount", "--mount-string", "/src:/mnt/src:ro",
		}
		assert.Equal(t, want, args)
	})
}
