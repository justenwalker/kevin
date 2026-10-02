package helm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
		err := fmt.Errorf("helm version: %w", exec.ErrNotFound)
		got := notInstalled(err)
		require.ErrorIs(t, got, err)
		assert.Equal(t, "helm isn't installed, or isn't on PATH - install it: https://helm.sh/docs/intro/install/",
			uerr.Display(got))
	})

	t.Run("leaves any other failure alone", func(t *testing.T) {
		err := errors.New("helm version: exit status 1")
		assert.Same(t, err, notInstalled(err))
	})
}

func TestUpgradeArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		args := upgradeArgs(UpgradeSpec{
			Release:    "demo",
			Chart:      "./charts/demo",
			Kubeconfig: "/tmp/kubeconfig",
		})
		want := []string{"upgrade", "--install", "demo", "./charts/demo", "--kubeconfig", "/tmp/kubeconfig"}
		assert.Equal(t, want, args)
	})

	t.Run("full", func(t *testing.T) {
		args := upgradeArgs(UpgradeSpec{
			Release:          "demo",
			Chart:            "nginx",
			Kubeconfig:       "/tmp/kubeconfig",
			Context:          "kind-demo",
			Repo:             "https://charts.example.com",
			Version:          "1.2.3",
			Namespace:        "web",
			CreateNamespace:  true,
			ValuesFiles:      []string{"a.yaml", "b.yaml"},
			PostRenderer:     "./post-render.sh",
			PostRendererArgs: []string{"--flag"},
			Wait:             5 * time.Minute,
			Atomic:           true,
		})
		want := []string{
			"upgrade", "--install", "demo", "nginx",
			"--kubeconfig", "/tmp/kubeconfig",
			"--kube-context", "kind-demo",
			"--repo", "https://charts.example.com",
			"--version", "1.2.3",
			"-n", "web",
			"--create-namespace",
			"-f", "a.yaml", "-f", "b.yaml",
			"--post-renderer", "./post-render.sh",
			"--post-renderer-args", "--flag",
			"--wait", "--timeout", "5m0s",
			"--atomic",
		}
		assert.Equal(t, want, args)
	})
}

func TestUninstallArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		args := uninstallArgs(UninstallSpec{
			Release:    "demo",
			Kubeconfig: "/tmp/kubeconfig",
		})
		want := []string{"uninstall", "demo", "--kubeconfig", "/tmp/kubeconfig"}
		assert.Equal(t, want, args)
	})

	t.Run("full", func(t *testing.T) {
		args := uninstallArgs(UninstallSpec{
			Release:    "demo",
			Kubeconfig: "/tmp/kubeconfig",
			Context:    "kind-demo",
			Namespace:  "web",
		})
		want := []string{
			"uninstall", "demo",
			"--kubeconfig", "/tmp/kubeconfig",
			"--kube-context", "kind-demo",
			"-n", "web",
		}
		assert.Equal(t, want, args)
	})
}

func TestIsReleaseNotFound(t *testing.T) {
	t.Run("helm's not found message", func(t *testing.T) {
		err := errors.New("helm uninstall demo --kubeconfig /tmp/kubeconfig: Error: uninstall: Release not found: release: not found: exit status 1")
		assert.True(t, isReleaseNotFound(err))
	})

	t.Run("an unrelated error", func(t *testing.T) {
		err := errors.New("helm uninstall demo --kubeconfig /tmp/kubeconfig: Error: Kubernetes cluster unreachable: exit status 1")
		assert.False(t, isReleaseNotFound(err))
	})
}

func TestClient(t *testing.T) {
	t.Run("UpgradeInstall returns standard output", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, []string{"helm", "upgrade", "--install", "demo", "./demo", "--kubeconfig", "/kc"}, cmd.Args)
				_, err := io.WriteString(cmd.Stdout, "released")
				return err
			})

		out, err := New(runner).UpgradeInstall(t.Context(), UpgradeSpec{Release: "demo", Chart: "./demo", Kubeconfig: "/kc"})
		require.NoError(t, err)
		assert.Equal(t, "released", out)
	})

	t.Run("UpgradeInstall includes helm's stderr in the error", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "chart not found\n")
				return errors.New("exit status 1")
			})

		_, err := New(runner).UpgradeInstall(t.Context(), UpgradeSpec{Release: "demo", Chart: "nope", Kubeconfig: "/kc"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "chart not found")
	})

	t.Run("Uninstall reports ErrReleaseNotFound", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "Error: uninstall: Release not found: release: not found")
				return errors.New("exit status 1")
			})

		_, err := New(runner).Uninstall(t.Context(), UninstallSpec{Release: "demo", Kubeconfig: "/kc"})
		require.ErrorIs(t, err, ErrReleaseNotFound)
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(exec.ErrNotFound)

		_, err := New(runner).Uninstall(t.Context(), UninstallSpec{Release: "demo", Kubeconfig: "/kc"})
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "helm isn't installed")
	})
}
