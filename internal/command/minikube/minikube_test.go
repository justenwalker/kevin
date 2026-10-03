package minikube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command/commandtest"
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

func TestClient(t *testing.T) {
	run := func(t *testing.T, runErr error, check func(cmd *exec.Cmd)) *Client {
		t.Helper()
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				if check != nil {
					check(cmd)
				}
				return runErr
			})
		return New(runner)
	}

	t.Run("Start runs with minikube's own home and kubeconfig", func(t *testing.T) {
		c := run(t, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, "minikube", cmd.Args[0])
			assert.Contains(t, cmd.Args, "start")
			assert.Contains(t, cmd.Env, "MINIKUBE_HOME=/home")
			assert.Contains(t, cmd.Env, "KUBECONFIG=/kc")
		})
		err := c.Start(t.Context(), StartSpec{Name: "demo", Home: "/home", Kubeconfig: "/kc"}, io.Discard, io.Discard)
		require.NoError(t, err)
	})

	t.Run("Start wraps a failure", func(t *testing.T) {
		c := run(t, errors.New("exit status 1"), nil)
		err := c.Start(t.Context(), StartSpec{Name: "demo"}, io.Discard, io.Discard)
		require.ErrorContains(t, err, "minikube: start")
	})

	t.Run("Delete names the profile", func(t *testing.T) {
		c := run(t, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"minikube", "delete", "-p", "demo"}, cmd.Args)
		})
		require.NoError(t, c.Delete(t.Context(), "demo", "/home", io.Discard))
	})

	t.Run("ImageLoad names the archive and profile", func(t *testing.T) {
		c := run(t, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"minikube", "image", "load", "/img.tar", "-p", "demo"}, cmd.Args)
		})
		require.NoError(t, c.ImageLoad(t.Context(), "demo", "/home", "/img.tar", io.Discard))
	})

	t.Run("ImageLoad wraps a failure", func(t *testing.T) {
		c := run(t, errors.New("exit status 1"), nil)
		err := c.ImageLoad(t.Context(), "demo", "/home", "/img.tar", io.Discard)
		require.ErrorContains(t, err, "minikube: image load")
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		c := run(t, exec.ErrNotFound, nil)
		err := c.Delete(t.Context(), "demo", "/home", io.Discard)
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "minikube isn't installed")
	})
}

func TestAvailable(t *testing.T) {
	onPath := func(t *testing.T) {
		t.Helper()
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, Binary), []byte("#!/bin/sh\n"), 0o755))
		t.Setenv("PATH", dir)
	}

	t.Run("reports a binary that is not on PATH", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())

		err := New(commandtest.NewMockRunner(t)).Available(t.Context())

		require.ErrorIs(t, err, ErrUnavailable)
	})

	t.Run("runs minikube version", func(t *testing.T) {
		onPath(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, []string{Binary, "version", "--short"}, cmd.Args)
				return nil
			})

		require.NoError(t, New(runner).Available(t.Context()))
	})

	t.Run("includes what minikube wrote to stderr", func(t *testing.T) {
		onPath(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "driver is broken\n")
				return errors.New("exit status 1")
			})

		err := New(runner).Available(t.Context())

		require.ErrorIs(t, err, ErrUnavailable)
		assert.ErrorContains(t, err, "driver is broken")
	})

	t.Run("reports a failure with no stderr", func(t *testing.T) {
		onPath(t)
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(errors.New("exit status 1"))

		err := New(runner).Available(t.Context())

		require.ErrorIs(t, err, ErrUnavailable)
		assert.ErrorContains(t, err, "minikube version --short")
	})
}

func TestError(t *testing.T) {
	assert.Equal(t, "minikube: the minikube command is unavailable", ErrUnavailable.Error())
}
