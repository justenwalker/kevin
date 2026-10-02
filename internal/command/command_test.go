package command

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	t.Run("copies streams, env, and dir onto the process", func(t *testing.T) {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		cmd := exec.CommandContext(t.Context(), "sh", "-c", `printf "$GREETING"; pwd >&2; read line; printf " $line"`)
		cmd.Env = []string{"GREETING=hello"}
		cmd.Dir = dir
		cmd.Stdin = bytes.NewBufferString("world\n")
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		require.NoError(t, Run(t.Context(), cmd))
		assert.Equal(t, "hello world", stdout.String())
		assert.Contains(t, stderr.String(), dir[len(dir)-10:])
	})

	t.Run("reports a failing exit", func(t *testing.T) {
		err := Run(t.Context(), exec.CommandContext(t.Context(), "sh", "-c", "exit 3"))
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		assert.Equal(t, 3, exit.ExitCode())
	})

	t.Run("reports a missing binary as not found", func(t *testing.T) {
		err := Run(t.Context(), exec.CommandContext(t.Context(), "kevin-no-such-binary"))
		require.ErrorIs(t, err, exec.ErrNotFound)
	})

	t.Run("cancels the process with the context", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := Run(ctx, exec.CommandContext(t.Context(), "sleep", "10"))
		require.Error(t, err)
		assert.Less(t, time.Since(start), 5*time.Second)
	})
}
