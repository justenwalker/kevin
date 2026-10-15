package docker

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command/commandtest"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/cri/clirt"
)

func TestDialect(t *testing.T) {
	t.Run("a network without IPv6 is created with --ipv6=false", func(t *testing.T) {
		var created []string
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				created = append(created, cmd.Args...)
				return nil
			})
		c := Client{Client: clirt.Client[flavor]{Runner: runner}}

		require.NoError(t, c.NetworkCreate(t.Context(), "n", cri.NetworkOptions{}))

		assert.Contains(t, created, "--ipv6=false")
	})
}
