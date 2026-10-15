package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/commandtest"
	"github.com/justenwalker/kevin/internal/run/runstate"
)

func TestRunStateFor(t *testing.T) {
	assert.Equal(t, filepath.Join("/proj", ".kevin", "run"), runStateFor("/proj", "").Dir())
	assert.Equal(t, filepath.Join("/proj", ".kevin", "staging", "run"), runStateFor("/proj", "Staging").Dir())
}

// deadPID starts and immediately reaps a throwaway process, returning a pid
// guaranteed not to be alive - a short-lived helper process, not something
// that needs its own cancellation context.
func deadPID(t *testing.T) int {
	t.Helper()
	dead := exec.Command("true") //nolint:noctx // throwaway helper process, exits on its own
	require.NoError(t, dead.Start())
	require.NoError(t, dead.Wait())
	return dead.Process.Pid
}

func TestWaitForAddrs(t *testing.T) {
	t.Run("prints the addresses once they appear", func(t *testing.T) {
		state := runstate.At(t.TempDir())
		require.NoError(t, state.WritePID(os.Getpid()))
		require.NoError(t, state.WriteAddrs(runstate.Addrs{ConsoleAddr: "127.0.0.1:1", HTTPProxyAddr: "127.0.0.1:2"}))

		err := waitForAddrs(t.Context(), state, runningProcess(t), "irrelevant.log", time.Second, time.Millisecond)
		require.NoError(t, err)
	})

	t.Run("errors if the process exits before writing addresses", func(t *testing.T) {
		proc := command.NewProcess(1, func() error { return errors.New("exit status 3") })

		err := waitForAddrs(t.Context(), runstate.At(t.TempDir()), proc, "some.log", time.Second, time.Millisecond)
		require.ErrorContains(t, err, "exit status 3")
		assert.Contains(t, err.Error(), "some.log")
	})

	t.Run("times out without error while the process is still starting", func(t *testing.T) {
		err := waitForAddrs(t.Context(), runstate.At(t.TempDir()), runningProcess(t), "some.log", 20*time.Millisecond, 5*time.Millisecond)
		require.NoError(t, err, "a slow start is not a failure - the process is still running")
	})
}

func TestBackgroundArgsArgv(t *testing.T) {
	tests := []struct {
		name string
		args backgroundArgs
		want []string
	}{
		{
			name: "minimal",
			args: backgroundArgs{dir: "/proj", engine: "docker"},
			want: []string{"run", "-C", "/proj", "--engine", "docker"},
		},
		{
			name: "every option set",
			args: backgroundArgs{
				dir: "/proj", name: "staging", tags: []string{"a=1", "b"}, engine: "podman",
				debug: true, keep: true, open: true,
			},
			want: []string{
				"run", "-C", "/proj", "--engine", "podman",
				"-e", "staging", "-t", "a=1", "-t", "b",
				"--debug", "--keep", "--open",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.args.argv(), "--detach must never appear in the child's argv")
		})
	}
}

// runningProcess is a Process that has not exited by the end of the test.
func runningProcess(t *testing.T) *command.Process {
	t.Helper()
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	return command.NewProcess(os.Getpid(), func() error { <-block; return nil })
}

func TestRunInBackground(t *testing.T) {
	args := backgroundArgs{dir: "/proj", engine: "docker"}

	t.Run("starts a detached child and reports its addresses", func(t *testing.T) {
		state := runstate.At(t.TempDir())
		require.NoError(t, state.WriteAddrs(runstate.Addrs{ConsoleAddr: "127.0.0.1:1", HTTPProxyAddr: "127.0.0.1:2"}))

		starter := commandtest.NewMockStarter(t)
		starter.EXPECT().Start(mock.Anything, mock.Anything).RunAndReturn(
			func(ctx context.Context, cmd *exec.Cmd) (*command.Process, error) {
				require.NoError(t, ctx.Err())
				assert.Equal(t, args.argv(), cmd.Args[1:])
				assert.NotNil(t, cmd.Stdout, "the child logs to the state dir")
				return runningProcess(t), nil
			})

		require.NoError(t, runInBackground(t.Context(), starter, state, args))
		assert.FileExists(t, filepath.Join(state.Dir(), logFileName))
	})

	t.Run("the child outlives the context of the caller", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		state := runstate.At(t.TempDir())
		require.NoError(t, state.WriteAddrs(runstate.Addrs{ConsoleAddr: "127.0.0.1:1"}))

		starter := commandtest.NewMockStarter(t)
		starter.EXPECT().Start(mock.Anything, mock.Anything).RunAndReturn(
			func(startCtx context.Context, _ *exec.Cmd) (*command.Process, error) {
				cancel()
				require.NoError(t, startCtx.Err(), "cancelling the caller must not kill the child")
				return runningProcess(t), nil
			})

		require.NoError(t, runInBackground(ctx, starter, state, args))
	})

	t.Run("wraps a failure to start", func(t *testing.T) {
		starter := commandtest.NewMockStarter(t)
		starter.EXPECT().Start(mock.Anything, mock.Anything).Return(nil, errors.New("no such file"))

		err := runInBackground(t.Context(), starter, runstate.At(t.TempDir()), args)
		require.ErrorContains(t, err, "start background process: no such file")
	})
}
