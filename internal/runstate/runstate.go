// Package runstate tracks the one live "kevin run" of a project environment:
// its pid, its lock, and the console and proxy addresses it listens on. It
// is files in the run directory, with no daemon, so a crashed run leaves
// state that the next call recognizes as stale and clears.
package runstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	pidFileName  = "kevin.pid"
	lockFileName = "kevin.lock"
	addrFileName = "kevin.json"
)

// stopTimeout bounds how long Stop waits for the run to exit.
const stopTimeout = 60 * time.Second

// Addrs are where a running environment listens.
type Addrs struct {
	ConsoleAddr   string `json:"console_addr"`
	HTTPProxyAddr string `json:"http_proxy_addr"`
}

// State is the run directory of one project environment.
type State struct {
	dir string
}

// At returns the State under workspace, the environment's own workspace
// directory. The run directory is a subdirectory of it.
func At(workspace string) State {
	return State{dir: filepath.Join(workspace, "run")}
}

// Dir returns the run directory. It also holds the log of a detached run.
func (s State) Dir() string { return s.dir }

// WriteAddrs stores a under the run directory.
func (s State) WriteAddrs(a Addrs) error {
	data, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("runstate: marshal addrs: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("runstate: create run dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, addrFileName), data, 0o600); err != nil {
		return fmt.Errorf("runstate: write addrs: %w", err)
	}
	return nil
}

// ReadAddrs reads back what WriteAddrs stored, or a zero value if none is
// stored yet.
func (s State) ReadAddrs() (Addrs, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, addrFileName))
	if errors.Is(err, os.ErrNotExist) {
		return Addrs{}, nil
	}
	if err != nil {
		return Addrs{}, fmt.Errorf("runstate: read addrs: %w", err)
	}
	var a Addrs
	if err := json.Unmarshal(data, &a); err != nil {
		return Addrs{}, fmt.Errorf("runstate: parse addrs: %w", err)
	}
	return a, nil
}

// WritePID stores pid under the run directory.
func (s State) WritePID(pid int) error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("runstate: create run dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, pidFileName), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("runstate: write pidfile: %w", err)
	}
	return nil
}

// Claim checks that no live run holds the state, then stores pid, under an
// exclusive lock so two runs started together cannot both pass the check.
// The lock file is never removed: unlinking it while another run waits on
// the lock would let a third run lock a fresh file and skip the wait.
func (s State) Claim(pid int) error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("runstate: create run dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(s.dir, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("runstate: open lock file: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("runstate: lock %s: %w", lockFileName, err)
	}
	if err = s.CheckNotRunning(); err != nil {
		return err
	}
	return s.WritePID(pid)
}

// PID reads the stored pid, reporting 0 when it is absent or unreadable:
// both mean that nothing is tracked as running here.
func (s State) PID() int {
	data, err := os.ReadFile(filepath.Join(s.dir, pidFileName))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// Alive reports whether the stored pid names a live process.
func (s State) Alive() bool {
	pid := s.PID()
	return pid != 0 && pidAlive(pid)
}

func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// Remove removes the pid and address files. A log in the run directory stays:
// it is a log, not live state.
func (s State) Remove() {
	_ = os.Remove(filepath.Join(s.dir, pidFileName))
	_ = os.Remove(filepath.Join(s.dir, addrFileName))
}

// CheckNotRunning fails with [ErrAlreadyRunning] when the stored pid names a
// live process. A stale pid, a dead process or nothing readable, is cleared
// instead of blocking the caller.
func (s State) CheckNotRunning() error {
	pid := s.PID()
	if pid == 0 {
		return nil
	}
	if pidAlive(pid) {
		return fmt.Errorf(`%w (pid %d); stop it with "kevin stop"`, ErrAlreadyRunning, pid)
	}
	s.Remove()
	return nil
}

// Running returns the addresses of the tracked run. It reports
// [ErrNotRunning] when nothing is tracked, and [ErrStaleRun], after clearing
// the leftover state, when the tracked process is gone.
func (s State) Running() (Addrs, error) {
	pid := s.PID()
	if pid == 0 {
		return Addrs{}, ErrNotRunning
	}
	if !pidAlive(pid) {
		s.Remove()
		return Addrs{}, ErrStaleRun
	}
	return s.ReadAddrs()
}

// Stop signals the tracked run the way an interrupt would, and waits for it
// to exit. It reports what it did to w.
func (s State) Stop(ctx context.Context, w io.Writer) error {
	pid := s.PID()
	if pid == 0 {
		_, _ = fmt.Fprintln(w, "not running")
		return nil
	}
	if !pidAlive(pid) {
		s.Remove()
		_, _ = fmt.Fprintln(w, "not running (removed stale pid file)")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("runstate: stop: %w", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("runstate: stop: signal pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(stopTimeout)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("runstate: stop: pid %d did not exit within %.0fs; check its log", pid, stopTimeout.Seconds())
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("runstate: stop: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
	_, _ = fmt.Fprintln(w, "stopped")
	return nil
}
