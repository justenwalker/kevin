package kubectl

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
		err := fmt.Errorf("kubectl version: %w", exec.ErrNotFound)
		got := notInstalled(err)
		require.ErrorIs(t, got, err)
		assert.Equal(t, "kubectl isn't installed, or isn't on PATH - install it: https://kubernetes.io/docs/tasks/tools/#kubectl",
			uerr.Display(got))
	})

	t.Run("leaves any other failure alone", func(t *testing.T) {
		err := errors.New("kubectl version: exit status 1")
		assert.Same(t, err, notInstalled(err))
	})
}

func TestApplyArgs(t *testing.T) {
	t.Run("a manifest uses stdin", func(t *testing.T) {
		args, stdin := applyArgs(ApplySpec{
			Kubeconfig: "/tmp/kubeconfig",
			Context:    "kind-demo",
			Namespace:  "demo",
			Manifest:   "apiVersion: v1\n",
			ServerSide: true,
		})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "--context", "kind-demo", "-n", "demo", "apply", "-f", "-", "--server-side"}
		assert.Equal(t, want, args)
		assert.NotNil(t, stdin, "expected stdin to carry the manifest")
	})

	t.Run("a path", func(t *testing.T) {
		args, stdin := applyArgs(ApplySpec{Kubeconfig: "/tmp/kubeconfig", Path: "manifests/app.yaml"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "apply", "-f", "manifests/app.yaml"}
		assert.Equal(t, want, args)
		assert.Nil(t, stdin, "expected no stdin for a path source")
	})

	t.Run("kustomize", func(t *testing.T) {
		args, _ := applyArgs(ApplySpec{Kubeconfig: "/tmp/kubeconfig", Kustomize: "overlays/dev"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "apply", "-k", "overlays/dev"}
		assert.Equal(t, want, args)
	})
}

func TestDeleteArgs(t *testing.T) {
	t.Run("a manifest uses stdin", func(t *testing.T) {
		args, stdin := deleteArgs(DeleteSpec{
			Kubeconfig: "/tmp/kubeconfig",
			Context:    "kind-demo",
			Namespace:  "demo",
			Manifest:   "apiVersion: v1\n",
		})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "--context", "kind-demo", "-n", "demo", "delete", "--ignore-not-found", "-f", "-"}
		assert.Equal(t, want, args)
		assert.NotNil(t, stdin, "expected stdin to carry the manifest")
	})

	t.Run("a path", func(t *testing.T) {
		args, stdin := deleteArgs(DeleteSpec{Kubeconfig: "/tmp/kubeconfig", Path: "manifests/app.yaml"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "delete", "--ignore-not-found", "-f", "manifests/app.yaml"}
		assert.Equal(t, want, args)
		assert.Nil(t, stdin, "expected no stdin for a path source")
	})

	t.Run("kustomize", func(t *testing.T) {
		args, _ := deleteArgs(DeleteSpec{Kubeconfig: "/tmp/kubeconfig", Kustomize: "overlays/dev"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "delete", "--ignore-not-found", "-k", "overlays/dev"}
		assert.Equal(t, want, args)
	})
}

func TestWaitArgs(t *testing.T) {
	t.Run("builds the for flag", func(t *testing.T) {
		args := waitArgs(WaitSpec{
			Kubeconfig: "/tmp/kubeconfig",
			Context:    "kind-demo",
			Namespace:  "demo",
			Resource:   "pod/mypod",
			For:        "condition=Ready",
			Timeout:    10 * time.Second,
		})
		want := []string{
			"--kubeconfig", "/tmp/kubeconfig", "--context", "kind-demo", "-n", "demo",
			"wait", "pod/mypod", "--for=condition=Ready", "--timeout=10s",
		}
		assert.Equal(t, want, args)
	})

	t.Run("omits timeout when zero", func(t *testing.T) {
		args := waitArgs(WaitSpec{Kubeconfig: "/tmp/kubeconfig", Resource: "pod/mypod", For: "condition=Ready"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "wait", "pod/mypod", "--for=condition=Ready"}
		assert.Equal(t, want, args)
	})
}

func TestRolloutStatusArgs(t *testing.T) {
	t.Run("builds every flag", func(t *testing.T) {
		args := rolloutStatusArgs(RolloutStatusSpec{
			Kubeconfig: "/tmp/kubeconfig",
			Context:    "kind-demo",
			Resource:   "deployment/api",
			Timeout:    30 * time.Second,
		})
		want := []string{
			"--kubeconfig", "/tmp/kubeconfig", "--context", "kind-demo",
			"rollout", "status", "deployment/api", "--timeout=30s",
		}
		assert.Equal(t, want, args)
	})

	t.Run("omits timeout when zero", func(t *testing.T) {
		args := rolloutStatusArgs(RolloutStatusSpec{Kubeconfig: "/tmp/kubeconfig", Resource: "deployment/api"})
		want := []string{"--kubeconfig", "/tmp/kubeconfig", "rollout", "status", "deployment/api"}
		assert.Equal(t, want, args)
	})
}

func TestClient(t *testing.T) {
	t.Run("Apply feeds an inline manifest through stdin", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, []string{"kubectl", "--kubeconfig", "/kc", "apply", "-f", "-"}, cmd.Args)
				in, err := io.ReadAll(cmd.Stdin)
				require.NoError(t, err)
				assert.Equal(t, "kind: Pod", string(in))
				_, err = io.WriteString(cmd.Stdout, "pod/x created")
				return err
			})

		out, err := New(runner).Apply(t.Context(), ApplySpec{Kubeconfig: "/kc", Manifest: "kind: Pod"})
		require.NoError(t, err)
		assert.Equal(t, "pod/x created", out)
	})

	t.Run("Delete, Wait, and RolloutStatus run their own subcommand", func(t *testing.T) {
		var got [][]string
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				got = append(got, cmd.Args)
				return nil
			}).Times(3)

		c := New(runner)
		_, err := c.Delete(t.Context(), DeleteSpec{Kubeconfig: "/kc", Path: "/m.yaml"})
		require.NoError(t, err)
		_, err = c.Wait(t.Context(), WaitSpec{Kubeconfig: "/kc", Resource: "pod/x", For: "condition=Ready"})
		require.NoError(t, err)
		_, err = c.RolloutStatus(t.Context(), RolloutStatusSpec{Kubeconfig: "/kc", Resource: "deployment/api"})
		require.NoError(t, err)

		require.Len(t, got, 3)
		assert.Contains(t, got[0], "delete")
		assert.Contains(t, got[1], "wait")
		assert.Contains(t, got[2], "rollout")
	})

	t.Run("includes kubectl's stderr in the error", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "no such resource\n")
				return errors.New("exit status 1")
			})

		_, err := New(runner).Wait(t.Context(), WaitSpec{Kubeconfig: "/kc", Resource: "pod/x", For: "delete"})
		require.ErrorContains(t, err, "no such resource")
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(exec.ErrNotFound)

		_, err := New(runner).Apply(t.Context(), ApplySpec{Kubeconfig: "/kc", Manifest: "x"})
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "kubectl isn't installed")
	})
}
