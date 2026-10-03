package command

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
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

func TestStart(t *testing.T) {
	t.Run("starts the process and sets it on cmd", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), "sh", "-c", "exit 0")
		proc, err := Start(t.Context(), cmd)
		require.NoError(t, err)
		assert.Positive(t, proc.Pid())
		require.NoError(t, proc.Wait())
		<-proc.Done()
	})

	t.Run("reaps a child that exits on its own", func(t *testing.T) {
		proc, err := Start(t.Context(), exec.CommandContext(t.Context(), "sh", "-c", "exit 1"))
		require.NoError(t, err)

		select {
		case <-proc.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("child was not waited on")
		}
		var exit *exec.ExitError
		require.ErrorAs(t, proc.Wait(), &exit, "a later Wait returns the same result")
		assert.Equal(t, 1, exit.ExitCode())
		// Signal 0 succeeds on an unreaped zombie.
		require.Error(t, syscall.Kill(proc.Pid(), 0))
	})

	t.Run("reports a missing binary as not found", func(t *testing.T) {
		_, err := Start(t.Context(), exec.CommandContext(t.Context(), "kevin-no-such-binary"))
		require.ErrorIs(t, err, exec.ErrNotFound)
	})
}

func TestNewProcess(t *testing.T) {
	t.Run("Done closes without a Wait call", func(t *testing.T) {
		p := NewProcess(1, func() error { return nil })

		select {
		case <-p.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("Done did not close")
		}
	})

	t.Run("every Wait returns the exit error", func(t *testing.T) {
		p := NewProcess(1, func() error { return assert.AnError })

		require.ErrorIs(t, p.Wait(), assert.AnError)
		require.ErrorIs(t, p.Wait(), assert.AnError)
	})
}
