package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/protos/pb"
)

const (
	pidFileName  = "kevin.pid"
	addrFileName = "kevin.json"
	logFileName  = "run.log"
)

// runStateDir returns the directory that holds one "kevin run"'s pidfile,
// address file, and (background mode only) log - a subdirectory of the
// project's workspace, keyed by the same slugged environment name the
// engine itself uses for its own workspace path.
func runStateDir(dir, name string) string {
	return filepath.Join(dir, engine.WorkspaceDir, config.SlugName(name), "run")
}

// runAddrs is the on-disk shape of addrFileName - just enough for "kevin
// stop" and a future caller to reach a backgrounded run without re-parsing
// terminal output.
type runAddrs struct {
	ConsoleAddr   string `json:"console_addr"`
	HTTPProxyAddr string `json:"http_proxy_addr"`
}

// writeRunAddrs stores env's console/proxy addresses under stateDir.
func writeRunAddrs(stateDir string, env *pb.Environment) error {
	data, err := json.Marshal(runAddrs{ConsoleAddr: env.GetConsoleAddr(), HTTPProxyAddr: env.GetHttpProxyAddr()})
	if err != nil {
		return fmt.Errorf("cmd: run: marshal addrs: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, addrFileName), data, 0o600); err != nil {
		return fmt.Errorf("cmd: run: write addrs: %w", err)
	}
	return nil
}

// readRunAddrs reads back what writeRunAddrs stored, or a zero value if
// stateDir holds none yet.
func readRunAddrs(stateDir string) (runAddrs, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, addrFileName)) //nolint:gosec // path is the project's own workspace file
	if errors.Is(err, os.ErrNotExist) {
		return runAddrs{}, nil
	}
	if err != nil {
		return runAddrs{}, fmt.Errorf("cmd: run: read addrs: %w", err)
	}
	var a runAddrs
	if err := json.Unmarshal(data, &a); err != nil {
		return runAddrs{}, fmt.Errorf("cmd: run: parse addrs: %w", err)
	}
	return a, nil
}

// writePID stores pid, this process's own, under stateDir.
func writePID(stateDir string, pid int) error {
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("cmd: run: create state dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, pidFileName), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return fmt.Errorf("cmd: run: write pidfile: %w", err)
	}
	return nil
}

// readPID reads stateDir's pidfile, reporting 0 when it's absent or
// unreadable - both mean the same thing to a caller: nothing is tracked as
// running here.
func readPID(stateDir string) int {
	data, err := os.ReadFile(filepath.Join(stateDir, pidFileName)) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// pidAlive reports whether pid names a live process.
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// removeRunState removes stateDir's pidfile and address file. run.log, if
// any, is left in place - it's a log, not live state.
func removeRunState(stateDir string) {
	_ = os.Remove(filepath.Join(stateDir, pidFileName))
	_ = os.Remove(filepath.Join(stateDir, addrFileName))
}

// checkNotRunning fails with ErrAlreadyRunning when stateDir's pidfile
// names a live process. A stale pidfile (a dead pid, or nothing readable)
// is cleared instead of blocking the caller.
func checkNotRunning(stateDir string) error {
	pid := readPID(stateDir)
	if pid == 0 {
		return nil
	}
	if pidAlive(pid) {
		return fmt.Errorf(`%w (pid %d); stop it with "kevin stop"`, ErrAlreadyRunning, pid)
	}
	removeRunState(stateDir)
	return nil
}

// backgroundArgs is what runInBackground needs to reconstruct the child's
// argv from already-parsed, typed values, not by re-slicing os.Args.
type backgroundArgs struct {
	dir    string
	name   string
	tags   []string
	engine string
	debug  bool
	keep   bool
	open   bool
}

// argv renders a into a "kevin run" argument list equivalent to the
// parent's own invocation, minus --detach and with engine pinned to
// the name the parent already resolved.
func (a backgroundArgs) argv() []string {
	args := []string{"run", "-C", a.dir, "--engine", a.engine}
	if a.name != "" {
		args = append(args, "-e", a.name)
	}
	for _, t := range a.tags {
		args = append(args, "-t", t)
	}
	if a.debug {
		args = append(args, "--debug")
	}
	if a.keep {
		args = append(args, "--keep")
	}
	if a.open {
		args = append(args, "--open")
	}
	return args
}

// addrsPollTimeout and addrsPollInterval bound waitForAddrs's poll for the
// address file after a --detach start.
const (
	addrsPollTimeout  = 10 * time.Second
	addrsPollInterval = 200 * time.Millisecond
)

// runInBackground starts a detached "kevin run" child for a and waits for
// it to either report its console/proxy addresses or exit early. The
// child's own invocation of runForeground does its pidfile bookkeeping,
// the same as a plain foreground run.
func runInBackground(ctx context.Context, stateDir string, a backgroundArgs) error {
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("cmd: run: create state dir: %w", err)
	}
	logPath := filepath.Join(stateDir, logFileName)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return fmt.Errorf("cmd: run: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cmd: run: find executable: %w", err)
	}
	// exec.Command, not exec.CommandContext: the child must outlive this
	// process's own ctx (a short-lived CLI invocation) - tying it to ctx
	// would kill the detached process the moment this command returns.
	child := exec.Command(exe, a.argv()...) //nolint:noctx,gosec // deliberately detached, see comment above; exe is this same binary, args are typed/reconstructed above
	child.Stdout = logFile
	child.Stderr = logFile
	detach(child)
	if err := child.Start(); err != nil {
		return fmt.Errorf("cmd: run: start background process: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stderr, "started in background (pid %d), logs: %s\n", child.Process.Pid, logPath)
	return waitForAddrs(ctx, stateDir, child.Process.Pid, logPath, addrsPollTimeout, addrsPollInterval)
}

// waitForAddrs polls for stateDir's address file and prints it the same way
// a foreground run would once it appears. It reports an error if pid exits
// first - the caller's exit code is then the only signal a script gets that
// --detach failed to start. Timing out with pid still alive is not an
// error: the environment may just be slow to come up, and it keeps running
// in the background regardless; waitForAddrs points at logPath instead.
func waitForAddrs(ctx context.Context, stateDir string, pid int, logPath string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if addrs, err := readRunAddrs(stateDir); err == nil && addrs.ConsoleAddr != "" {
			printEnvironmentInfo(os.Stderr, &pb.Environment{ConsoleAddr: addrs.ConsoleAddr, HttpProxyAddr: addrs.HTTPProxyAddr})
			return nil
		}
		if !pidAlive(pid) {
			return fmt.Errorf("cmd: run: background process exited before starting up; see %s", logPath)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "still starting; check %s or the console once it's up\n", logPath)
	return nil
}

// stopRun signals stateDir's tracked process (if any) the same way an
// interrupt would, and waits for it to exit.
func stopRun(ctx context.Context, w io.Writer, stateDir string) error {
	pid := readPID(stateDir)
	if pid == 0 {
		_, _ = fmt.Fprintln(w, "not running")
		return nil
	}
	if !pidAlive(pid) {
		removeRunState(stateDir)
		_, _ = fmt.Fprintln(w, "not running (removed stale pid file)")
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("cmd: stop: %w", err)
	}
	if err := proc.Signal(os.Interrupt); err != nil {
		return fmt.Errorf("cmd: stop: signal pid %d: %w", pid, err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for pidAlive(pid) {
		if time.Now().After(deadline) {
			return fmt.Errorf("cmd: stop: pid %d did not exit within 60s; check its log", pid)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cmd: stop: %w", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
	_, _ = fmt.Fprintln(w, "stopped")
	return nil
}
