package podman

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command/commandtest"
	"github.com/justenwalker/kevin/internal/container"
	"github.com/justenwalker/kevin/internal/container/clirt"
	"github.com/justenwalker/kevin/internal/uerr"
)

func TestSocketPath(t *testing.T) {
	t.Run("a plain path", func(t *testing.T) {
		got, err := socketPath("/run/user/1000/podman/podman.sock\n")
		require.NoError(t, err)
		assert.Equal(t, "/run/user/1000/podman/podman.sock", got)
	})

	t.Run("a unix URI", func(t *testing.T) {
		got, err := socketPath("unix:///run/podman/podman.sock\n")
		require.NoError(t, err)
		assert.Equal(t, "/run/podman/podman.sock", got)
	})

	t.Run("no socket", func(t *testing.T) {
		for _, out := range []string{"", "\n", "<no value>\n"} {
			_, err := socketPath(out)
			require.ErrorIs(t, err, ErrNoSocket, "output %q", out)
		}
	})
}

func TestDialect(t *testing.T) {
	t.Run("a network without IPv6 is created with no IPv6 flag", func(t *testing.T) {
		var created []string
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				created = append(created, cmd.Args...)
				return nil
			})
		c := Client{Client: clirt.Client[flavor]{Runner: runner}}

		require.NoError(t, c.NetworkCreate(t.Context(), "n", container.NetworkOptions{}))

		assert.NotContains(t, created, "--ipv6=false")
		assert.NotContains(t, created, "--ipv6")
	})

	t.Run("an unauthorized pull is explained as a missing image", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "unauthorized: authentication required\n")
				return errors.New("exit status 125")
			})
		c := Client{Client: clirt.Client[flavor]{Runner: runner}}

		_, err := c.Run(t.Context(), container.RunSpec{Name: "x", Image: "acme/widget"})

		require.Error(t, err)
		assert.Contains(t, uerr.Display(err), "couldn't be found or pulled")
	})
}

func TestDockerHost(t *testing.T) {
	run := func(t *testing.T, out string, err error) (string, error) {
		t.Helper()
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stdout, out)
				return err
			})
		return Client{Client: clirt.Client[flavor]{Runner: runner}}.DockerHost(t.Context())
	}

	t.Run("prefixes the socket path with unix://", func(t *testing.T) {
		got, err := run(t, "/run/podman/podman.sock\n", nil)

		require.NoError(t, err)
		assert.Equal(t, "unix:///run/podman/podman.sock", got)
	})

	t.Run("podman naming no socket reports ErrNoSocket", func(t *testing.T) {
		_, err := run(t, "\n", nil)

		require.ErrorIs(t, err, ErrNoSocket)
	})

	t.Run("a failing podman is an error", func(t *testing.T) {
		_, err := run(t, "", errors.New("exit status 1"))

		require.Error(t, err)
	})
}
