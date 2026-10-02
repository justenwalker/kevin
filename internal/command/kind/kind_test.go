package kind

import (
	"bytes"
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
	args := deleteArgs(DeleteSpec{
		Name: "demo", Kubeconfig: "/tmp/kubeconfig",
		Env: map[string]string{"KIND_EXPERIMENTAL_PROVIDER": "podman"},
	})
	want := []string{"delete", "cluster", "--name", "demo", "--kubeconfig", "/tmp/kubeconfig"}
	assert.Equal(t, want, args, "Env sets the child process environment, not a command-line flag")
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

func TestClient(t *testing.T) {
	// run answers the one call a test expects, handing cmd to check first.
	run := func(t *testing.T, stdout string, runErr error, check func(cmd *exec.Cmd)) *Client {
		t.Helper()
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				if check != nil {
					check(cmd)
				}
				if cmd.Stdout != nil {
					_, _ = io.WriteString(cmd.Stdout, stdout)
				}
				return runErr
			})
		return New(runner)
	}

	t.Run("Create feeds the config on stdin and streams output", func(t *testing.T) {
		var stdout bytes.Buffer
		c := run(t, "creating\n", nil, func(cmd *exec.Cmd) {
			assert.Equal(t, "kind", cmd.Args[0])
			in, err := io.ReadAll(cmd.Stdin)
			require.NoError(t, err)
			assert.Equal(t, "kind: Cluster", string(in))
			assert.Contains(t, cmd.Env, "KIND_EXPERIMENTAL_PROVIDER=podman")
		})

		err := c.Create(t.Context(), CreateSpec{
			Name: "demo", Kubeconfig: "/kc", Config: "kind: Cluster",
			Env: map[string]string{"KIND_EXPERIMENTAL_PROVIDER": "podman"},
		}, &stdout, io.Discard)
		require.NoError(t, err)
		assert.Equal(t, "creating\n", stdout.String())
	})

	t.Run("Create wraps a failure", func(t *testing.T) {
		c := run(t, "", errors.New("exit status 1"), nil)
		err := c.Create(t.Context(), CreateSpec{Name: "demo"}, io.Discard, io.Discard)
		require.ErrorContains(t, err, "kind: create cluster")
	})

	t.Run("Delete names the cluster and kubeconfig", func(t *testing.T) {
		c := run(t, "", nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"kind", "delete", "cluster", "--name", "demo", "--kubeconfig", "/kc"}, cmd.Args)
		})
		require.NoError(t, c.Delete(t.Context(), DeleteSpec{Name: "demo", Kubeconfig: "/kc"}, io.Discard))
	})

	t.Run("GetNodes returns one name per line", func(t *testing.T) {
		c := run(t, "demo-control-plane\ndemo-worker\n\n", nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"kind", "get", "nodes", "--name", "demo"}, cmd.Args)
		})
		got, err := c.GetNodes(t.Context(), "demo", nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"demo-control-plane", "demo-worker"}, got)
	})

	t.Run("GetClusters returns one name per line", func(t *testing.T) {
		c := run(t, "a\nb\n", nil, nil)
		got, err := c.GetClusters(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []string{"a", "b"}, got)
	})

	t.Run("LoadImageArchive names the archive and cluster", func(t *testing.T) {
		c := run(t, "", nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"kind", "load", "image-archive", "/img.tar", "--name", "demo"}, cmd.Args)
		})
		require.NoError(t, c.LoadImageArchive(t.Context(), LoadImageArchiveSpec{Name: "demo", Path: "/img.tar"}, io.Discard))
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		c := run(t, "", exec.ErrNotFound, nil)
		_, err := c.GetNodes(t.Context(), "demo", nil)
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "kind isn't installed")
	})
}
