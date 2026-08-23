package kindcmd

import (
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/uerr"
)

func TestNotInstalled(t *testing.T) {
	t.Run("attaches a message when the binary is missing", func(t *testing.T) {
		err := fmt.Errorf("kind version: %w", exec.ErrNotFound)
		got := notInstalled(err)
		require.ErrorIs(t, got, err)
		assert.Equal(t, "kind isn't installed, or isn't on PATH - install it: https://kind.sigs.k8s.io/docs/user/quick-start/#installation",
			uerr.Display(got))
	})

	t.Run("leaves any other failure alone", func(t *testing.T) {
		err := errors.New("kind version: exit status 1")
		assert.Same(t, err, notInstalled(err))
	})
}

func TestCreateArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		args := createArgs(CreateSpec{Name: "demo", Kubeconfig: "/tmp/kubeconfig"})
		want := []string{"create", "cluster", "--name", "demo", "--config", "-", "--kubeconfig", "/tmp/kubeconfig"}
		assert.Equal(t, want, args)
	})

	t.Run("full", func(t *testing.T) {
		args := createArgs(CreateSpec{
			Name:       "demo",
			Kubeconfig: "/tmp/kubeconfig",
			Wait:       5 * time.Minute,
			Retain:     true,
			Image:      "kindest/node:v1.30.0",
		})
		want := []string{
			"create", "cluster",
			"--name", "demo",
			"--config", "-",
			"--kubeconfig", "/tmp/kubeconfig",
			"--wait", "5m0s",
			"--retain",
			"--image", "kindest/node:v1.30.0",
		}
		assert.Equal(t, want, args)
	})
}

func TestDeleteArgs(t *testing.T) {
	args := deleteArgs(DeleteSpec{Name: "demo", Kubeconfig: "/tmp/kubeconfig"})
	want := []string{"delete", "cluster", "--name", "demo", "--kubeconfig", "/tmp/kubeconfig"}
	assert.Equal(t, want, args)
}

func TestParseLines(t *testing.T) {
	t.Run("several nodes", func(t *testing.T) {
		names := parseLines("demo-control-plane\ndemo-worker\ndemo-worker2\n")
		assert.Equal(t, []string{"demo-control-plane", "demo-worker", "demo-worker2"}, names)
	})

	t.Run("blank lines are skipped", func(t *testing.T) {
		names := parseLines("demo-control-plane\n\n\n")
		assert.Equal(t, []string{"demo-control-plane"}, names)
	})

	t.Run("no nodes", func(t *testing.T) {
		names := parseLines("")
		assert.Nil(t, names)
	})
}

func TestEnvWith(t *testing.T) {
	t.Run("no extra vars inherits the process environment unchanged", func(t *testing.T) {
		assert.Nil(t, envWith(nil))
		assert.Nil(t, envWith(map[string]string{}))
	})

	t.Run("extra vars are appended", func(t *testing.T) {
		env := envWith(map[string]string{"HTTP_PROXY": "http://127.0.0.1:8080"})
		assert.Contains(t, env, "HTTP_PROXY=http://127.0.0.1:8080")
	})
}
