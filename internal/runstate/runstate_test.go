package runstate

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadPID starts and immediately reaps a throwaway process, returning a pid
// guaranteed not to be alive.
func deadPID(t *testing.T) int {
	t.Helper()
	dead := exec.Command("true") //nolint:noctx // throwaway helper process, exits on its own
	require.NoError(t, dead.Start())
	require.NoError(t, dead.Wait())
	return dead.Process.Pid
}

func newState(t *testing.T) State {
	t.Helper()
	return At(t.TempDir())
}

func TestAt(t *testing.T) {
	assert.Equal(t, filepath.Join("/ws", "run"), At("/ws").Dir())
}

func TestPID(t *testing.T) {
	t.Run("is 0 without a pidfile", func(t *testing.T) {
		assert.Equal(t, 0, newState(t).PID())
	})

	t.Run("reads back what WritePID stored", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(4242))
		assert.Equal(t, 4242, s.PID())
	})

	t.Run("is 0 for a corrupt file", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(1))
		require.NoError(t, os.WriteFile(filepath.Join(s.Dir(), pidFileName), []byte("not a pid"), 0o600))
		assert.Equal(t, 0, s.PID())
	})

	t.Run("WritePID fails when the run directory cannot be created", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores permission bits")
		}
		parent := t.TempDir()
		require.NoError(t, os.Chmod(parent, 0o500))
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

		require.Error(t, At(parent).WritePID(os.Getpid()))
	})
}

func TestAlive(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		assert.False(t, newState(t).Alive())
	})

	t.Run("a dead pid", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(deadPID(t)))
		assert.False(t, s.Alive())
	})

	t.Run("a live pid", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(os.Getpid()))
		assert.True(t, s.Alive())
	})
}

func TestAddrs(t *testing.T) {
	s := newState(t)
	got, err := s.ReadAddrs()
	require.NoError(t, err)
	assert.Zero(t, got, "no address file yet")

	want := Addrs{ConsoleAddr: "127.0.0.1:1", HTTPProxyAddr: "127.0.0.1:2"}
	require.NoError(t, s.WriteAddrs(want))
	got, err = s.ReadAddrs()
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestRemove(t *testing.T) {
	s := newState(t)
	require.NoError(t, s.WritePID(os.Getpid()))
	require.NoError(t, s.WriteAddrs(Addrs{ConsoleAddr: "x"}))

	s.Remove()

	assert.NoFileExists(t, filepath.Join(s.Dir(), pidFileName))
	assert.NoFileExists(t, filepath.Join(s.Dir(), addrFileName))
}

func TestCheckNotRunning(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		assert.NoError(t, newState(t).CheckNotRunning())
	})

	t.Run("live pid fails with ErrAlreadyRunning", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(os.Getpid()))

		err := s.CheckNotRunning()
		require.ErrorIs(t, err, ErrAlreadyRunning)
		assert.Contains(t, err.Error(), "kevin stop")
	})

	t.Run("stale pid is cleared, not an error", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(deadPID(t)))
		require.NoError(t, s.WriteAddrs(Addrs{ConsoleAddr: "x"}))

		require.NoError(t, s.CheckNotRunning())
		assert.NoFileExists(t, filepath.Join(s.Dir(), pidFileName))
		assert.NoFileExists(t, filepath.Join(s.Dir(), addrFileName))
	})
}

func TestClaim(t *testing.T) {
	t.Run("only one concurrent claim wins", func(t *testing.T) {
		s := newState(t)
		const claimers = 16
		errs := make(chan error, claimers)
		var wg sync.WaitGroup
		for range claimers {
			wg.Go(func() { errs <- s.Claim(os.Getpid()) })
		}
		wg.Wait()
		close(errs)

		wins := 0
		for err := range errs {
			if err == nil {
				wins++
				continue
			}
			require.ErrorIs(t, err, ErrAlreadyRunning)
		}
		assert.Equal(t, 1, wins)
	})

	t.Run("a stale pid is taken over", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(deadPID(t)))

		require.NoError(t, s.Claim(os.Getpid()))
		assert.Equal(t, os.Getpid(), s.PID())
	})
}

func TestRunning(t *testing.T) {
	t.Run("nothing tracked", func(t *testing.T) {
		_, err := newState(t).Running()
		require.ErrorIs(t, err, ErrNotRunning)
	})

	t.Run("a stale run is cleared", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(deadPID(t)))

		_, err := s.Running()

		require.ErrorIs(t, err, ErrStaleRun)
		assert.Zero(t, s.PID())
	})

	t.Run("a live run reports its addresses", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(os.Getpid()))
		want := Addrs{ConsoleAddr: "127.0.0.1:1"}
		require.NoError(t, s.WriteAddrs(want))

		got, err := s.Running()

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
}

func TestStop(t *testing.T) {
	t.Run("no pidfile", func(t *testing.T) {
		var buf bytes.Buffer
		require.NoError(t, newState(t).Stop(t.Context(), &buf))
		assert.Equal(t, "not running\n", buf.String())
	})

	t.Run("stale pidfile is cleared", func(t *testing.T) {
		s := newState(t)
		require.NoError(t, s.WritePID(deadPID(t)))

		var buf bytes.Buffer
		require.NoError(t, s.Stop(t.Context(), &buf))
		assert.Equal(t, "not running (removed stale pid file)\n", buf.String())
		assert.NoFileExists(t, filepath.Join(s.Dir(), pidFileName))
	})

	t.Run("signals a live process and waits for it to exit", func(t *testing.T) {
		s := newState(t)
		proc := exec.Command("sleep", "30") //nolint:noctx // killed explicitly in t.Cleanup, no context needed
		require.NoError(t, proc.Start())
		t.Cleanup(func() { _ = proc.Process.Kill() })
		// A signaled process stays visible to kill(pid, 0) as a zombie until
		// something reaps it. Real usage relies on the target's own parent
		// doing that; here it is this test, in the background so it does not
		// race Stop's own signal.
		go func() { _ = proc.Wait() }()
		require.NoError(t, s.WritePID(proc.Process.Pid))

		var buf bytes.Buffer
		require.NoError(t, s.Stop(t.Context(), &buf))
		assert.Equal(t, "stopped\n", buf.String())
	})
}
