package k3d

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
		err := fmt.Errorf("k3d version: %w", exec.ErrNotFound)
		got := notInstalled(err)
		require.ErrorIs(t, got, err)
		assert.Equal(t, "k3d isn't installed, or isn't on PATH - install it: https://k3d.io/#installation",
			uerr.Display(got))
	})

	t.Run("leaves any other failure alone", func(t *testing.T) {
		err := errors.New("k3d version: exit status 1")
		assert.Same(t, err, notInstalled(err))
	})
}

func TestEnvWith(t *testing.T) {
	t.Run("no extra variables inherits the environment", func(t *testing.T) {
		assert.Nil(t, envWith(nil))
	})

	t.Run("adds the variables to the process environment", func(t *testing.T) {
		t.Setenv("K3DCMD_TEST_KEEP", "yes")

		got := envWith(map[string]string{"DOCKER_HOST": "unix:///run/podman.sock"})

		assert.Contains(t, got, "DOCKER_HOST=unix:///run/podman.sock")
		assert.Contains(t, got, "K3DCMD_TEST_KEEP=yes")
	})
}

func TestCreateArgs(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		args := CreateArgs(CreateSpec{Name: "demo", Network: "demo-net"})
		want := []string{
			"cluster", "create", "demo",
			"--servers", "1",
			"--agents", "0",
			"--network", "demo-net",
			"--kubeconfig-update-default=false",
			"--kubeconfig-switch-context=false",
			"--wait",
		}
		assert.Equal(t, want, args)
	})

	t.Run("full", func(t *testing.T) {
		args := CreateArgs(CreateSpec{
			Name:       "demo",
			Network:    "demo-net",
			Image:      "rancher/k3s:v1.34.1-k3s1",
			APIPort:    6550,
			NoRollback: true,
			Agents:     2,
			Wait:       5 * time.Minute,
			Env: map[string]string{
				"HTTP_PROXY": "http://127.0.0.1:8080",
				"NO_PROXY":   "localhost",
			},
			Volumes:    []string{"/ca.pem:/etc/ssl/certs/kevin-root.crt@server:*;agent:*"},
			NodeLabels: []string{"kevin.node=gpu@agent:0"},
			K3sArgs:    []string{"--cluster-cidr=10.42.0.0/16@server:*"},
			Memory:     "2g",
		})
		want := []string{
			"cluster", "create", "demo",
			"--servers", "1",
			"--agents", "2",
			"--network", "demo-net",
			"--kubeconfig-update-default=false",
			"--kubeconfig-switch-context=false",
			"--wait",
			"--timeout", "5m0s",
			"--image", "rancher/k3s:v1.34.1-k3s1",
			"--api-port", "127.0.0.1:6550",
			"--no-rollback",
			"--servers-memory", "2g",
			"--agents-memory", "2g",
			"--env", "HTTP_PROXY=http://127.0.0.1:8080@all",
			"--env", "NO_PROXY=localhost@all",
			"--volume", "/ca.pem:/etc/ssl/certs/kevin-root.crt@server:*;agent:*",
			"--k3s-node-label", "kevin.node=gpu@agent:0",
			"--k3s-arg", "--cluster-cidr=10.42.0.0/16@server:*",
		}
		assert.Equal(t, want, args)
	})
}

func TestParseNodes(t *testing.T) {
	const out = `[
	  {"name":"other","nodes":[{"name":"k3d-other-server-0","role":"server"}]},
	  {"name":"demo","nodes":[
	    {"name":"k3d-demo-serverlb","role":"loadbalancer"},
	    {"name":"k3d-demo-agent-1","role":"agent"},
	    {"name":"k3d-demo-server-0","role":"server"},
	    {"name":"k3d-demo-agent-0","role":"agent"}
	  ]}
	]`

	t.Run("servers then agents, without the load balancer", func(t *testing.T) {
		got, err := parseNodes(out, "demo")
		require.NoError(t, err)
		assert.Equal(t, []string{"k3d-demo-server-0", "k3d-demo-agent-0", "k3d-demo-agent-1"}, got)
	})

	t.Run("a missing cluster has no nodes", func(t *testing.T) {
		got, err := parseNodes(out, "absent")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("no clusters at all", func(t *testing.T) {
		got, err := parseNodes("null", "demo")
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("rejects malformed output", func(t *testing.T) {
		_, err := parseNodes("not json", "demo")
		require.Error(t, err)
	})
}

func TestClient(t *testing.T) {
	// run answers each expected call in turn: stdouts[i] is what call i
	// prints, and check sees every cmd first.
	run := func(t *testing.T, stdouts []string, runErr error, check func(cmd *exec.Cmd)) *Client {
		t.Helper()
		calls := 0
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				if check != nil {
					check(cmd)
				}
				if cmd.Stdout != nil && calls < len(stdouts) {
					_, _ = io.WriteString(cmd.Stdout, stdouts[calls])
				}
				calls++
				return runErr
			}).Times(max(len(stdouts), 1))
		return New(runner)
	}

	const list = `[{"name":"demo","nodes":[{"name":"k3d-demo-server-0","role":"server"}]}]`

	t.Run("GetNodes lists the cluster's nodes as json", func(t *testing.T) {
		c := run(t, []string{list}, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"k3d", "cluster", "list", "-o", "json"}, cmd.Args)
			assert.Contains(t, cmd.Env, "DOCKER_HOST=unix:///sock")
		})
		got, err := c.GetNodes(t.Context(), "demo", map[string]string{"DOCKER_HOST": "unix:///sock"})
		require.NoError(t, err)
		assert.Equal(t, []string{"k3d-demo-server-0"}, got)
	})

	t.Run("Delete skips a cluster that does not exist", func(t *testing.T) {
		c := run(t, []string{"[]"}, nil, nil)
		require.NoError(t, c.Delete(t.Context(), "demo", nil, io.Discard))
	})

	t.Run("Delete removes a cluster that exists", func(t *testing.T) {
		var got [][]string
		c := run(t, []string{list, ""}, nil, func(cmd *exec.Cmd) { got = append(got, cmd.Args) })
		require.NoError(t, c.Delete(t.Context(), "demo", nil, io.Discard))
		assert.Equal(t, []string{"k3d", "cluster", "delete", "demo"}, got[1])
	})

	t.Run("Delete stops when the listing fails", func(t *testing.T) {
		c := run(t, nil, errors.New("exit status 1"), nil)
		require.ErrorContains(t, c.Delete(t.Context(), "demo", nil, io.Discard), "k3d: cluster list")
	})

	t.Run("KubeconfigWrite names the output file", func(t *testing.T) {
		c := run(t, []string{""}, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"k3d", "kubeconfig", "write", "demo", "--output", "/kc", "--overwrite"}, cmd.Args)
		})
		require.NoError(t, c.KubeconfigWrite(t.Context(), "demo", "/kc", nil))
	})

	t.Run("ImageImport names the archive and cluster", func(t *testing.T) {
		c := run(t, nil, nil, func(cmd *exec.Cmd) {
			assert.Equal(t, []string{"k3d", "image", "import", "/img.tar", "--cluster", "demo"}, cmd.Args)
		})
		require.NoError(t, c.ImageImport(t.Context(), ImageImportSpec{Name: "demo", Path: "/img.tar"}, io.Discard))
	})

	t.Run("Create streams output and wraps a failure", func(t *testing.T) {
		c := run(t, nil, errors.New("exit status 1"), nil)
		err := c.Create(t.Context(), CreateSpec{Name: "demo"}, io.Discard, io.Discard)
		require.ErrorContains(t, err, "k3d: create cluster")
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		c := run(t, nil, exec.ErrNotFound, nil)
		err := c.ImageImport(t.Context(), ImageImportSpec{Name: "demo", Path: "/img.tar"}, io.Discard)
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "k3d isn't installed")
	})
}
