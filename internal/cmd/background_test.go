package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestRunStateDir(t *testing.T) {
	assert.Equal(t, filepath.Join("/proj", ".kevin", "run"), runStateDir("/proj", ""))
	assert.Equal(t, filepath.Join("/proj", ".kevin", "staging", "run"), runStateDir("/proj", "Staging"))
}

func TestPIDFile(t *testing.T) {
	dir := t.TempDir()
	assert.Equal(t, 0, readPID(dir), "no pidfile yet")

	require.NoError(t, writePID(dir, 4242))
	assert.Equal(t, 4242, readPID(dir))
}

func TestReadPIDCorruptFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, pidFileName), []byte("not a pid"), 0o600))
	assert.Equal(t, 0, readPID(dir))
}

func TestWritePIDCreateStateDirFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	parent := t.TempDir()
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	err := writePID(filepath.Join(parent, "run"), os.Getpid())
	require.Error(t, err)
}

// deadPID starts and immediately reaps a throwaway process, returning a pid
// guaranteed not to be alive - a short-lived helper process, not something
// that needs its own cancellation context.
func deadPID(t *testing.T) int {
	t.Helper()
	dead := exec.Command(os.Args[0], "-test.run=^$") //nolint:noctx // throwaway helper process, exits on its own
	require.NoError(t, dead.Start())
	require.NoError(t, dead.Wait())
	return dead.Process.Pid
}

func TestPidAlive(t *testing.T) {
	assert.True(t, pidAlive(os.Getpid()))
	assert.False(t, pidAlive(deadPID(t)))
}

func TestRunAddrs(t *testing.T) {
	dir := t.TempDir()
	got, err := readRunAddrs(dir)
	require.NoError(t, err)
	assert.Zero(t, got, "no address file yet")

	require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: "127.0.0.1:1", HttpProxyAddr: "127.0.0.1:2"}))
	got, err = readRunAddrs(dir)
	require.NoError(t, err)
	assert.Equal(t, runAddrs{ConsoleAddr: "127.0.0.1:1", HTTPProxyAddr: "127.0.0.1:2"}, got)
}

func TestWaitForAddrs(t *testing.T) {
	t.Run("prints the addresses once they appear", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: "127.0.0.1:1", HttpProxyAddr: "127.0.0.1:2"}))

		err := waitForAddrs(t.Context(), dir, os.Getpid(), "irrelevant.log", time.Second, time.Millisecond)
		require.NoError(t, err)
	})

	t.Run("errors if the process exits before writing addresses", func(t *testing.T) {
		dir := t.TempDir()
		err := waitForAddrs(t.Context(), dir, deadPID(t), "some.log", time.Second, time.Millisecond)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "some.log")
	})

	t.Run("times out without error while the process is still starting", func(t *testing.T) {
		dir := t.TempDir()
		err := waitForAddrs(t.Context(), dir, os.Getpid(), "some.log", 20*time.Millisecond, 5*time.Millisecond)
		require.NoError(t, err, "a slow start is not a failure - the process is still running")
	})
}

func TestRemoveRunState(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writePID(dir, os.Getpid()))
	require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: "x"}))

	removeRunState(dir)

	assert.NoFileExists(t, filepath.Join(dir, pidFileName))
	assert.NoFileExists(t, filepath.Join(dir, addrFileName))
}

func TestCheckNotRunning(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		assert.NoError(t, checkNotRunning(t.TempDir()))
	})

	t.Run("live pid fails with ErrAlreadyRunning", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writePID(dir, os.Getpid()))

		err := checkNotRunning(dir)
		require.ErrorIs(t, err, ErrAlreadyRunning)
		assert.Contains(t, err.Error(), "kevin stop")
	})

	t.Run("stale pid is cleared, not an error", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writePID(dir, deadPID(t)))
		require.NoError(t, writeRunAddrs(dir, &pb.Environment{ConsoleAddr: "x"}))

		require.NoError(t, checkNotRunning(dir))
		assert.NoFileExists(t, filepath.Join(dir, pidFileName))
		assert.NoFileExists(t, filepath.Join(dir, addrFileName))
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

func TestStopRun(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, stopRun(t.Context(), &buf, t.TempDir()))
		assert.Equal(t, "not running\n", buf.String())
	})

	t.Run("stale pidfile is cleared", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, writePID(dir, deadPID(t)))

		var buf bytes.Buffer
		require.NoError(t, stopRun(t.Context(), &buf, dir))
		assert.Equal(t, "not running (removed stale pid file)\n", buf.String())
		assert.NoFileExists(t, filepath.Join(dir, pidFileName))
	})

	t.Run("signals a live process and waits for it to exit", func(t *testing.T) {
		dir := t.TempDir()
		proc := exec.Command("sleep", "30") //nolint:noctx // killed explicitly in t.Cleanup, no context needed
		require.NoError(t, proc.Start())
		t.Cleanup(func() { _ = proc.Process.Kill() })
		// A signaled process stays visible to kill(pid, 0) as a zombie
		// until something reaps it - real usage relies on the target's own
		// parent (usually init, once the original parent exited) doing
		// that; here that's this test, in the background so it doesn't
		// race stopRun's own signal.
		go func() { _ = proc.Wait() }()
		require.NoError(t, writePID(dir, proc.Process.Pid))

		var buf bytes.Buffer
		require.NoError(t, stopRun(t.Context(), &buf, dir))
		assert.Equal(t, "stopped\n", buf.String())
	})
}
