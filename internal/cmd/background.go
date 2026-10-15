package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/runstate"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/internal/step"
)

// logFileName is the log of a detached run, in the run directory.
const logFileName = "run.log"

// runStateFor returns the run state of the project environment named name
// in dir, keyed by the same slugged name the engine uses for its workspace.
func runStateFor(dir, name string) runstate.State {
	return runstate.At(state.For(dir, config.SlugName(name)).Workspace())
}

// backgroundArgs is what runInBackground needs to reconstruct the child's
// argv from already-parsed, typed values, not by re-slicing os.Args.
type backgroundArgs struct {
	dir     string
	name    string
	tags    []string
	engine  string
	debug   bool
	keep    bool
	open    bool
	varFile string
	vars    []string
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
	if a.varFile != "" {
		args = append(args, "--var-file", a.varFile)
	}
	for _, v := range a.vars {
		args = append(args, "--var", v)
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
func runInBackground(ctx context.Context, starter command.Starter, state runstate.State, a backgroundArgs) error {
	if err := os.MkdirAll(state.Dir(), 0o750); err != nil {
		return fmt.Errorf("cmd: run: create state dir: %w", err)
	}
	logPath := filepath.Join(state.Dir(), logFileName)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return fmt.Errorf("cmd: run: open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cmd: run: find executable: %w", err)
	}
	// The child must outlive this process's own ctx (a short-lived CLI
	// invocation) - tying it to ctx would kill the detached process the
	// moment this command returns.
	detached := context.WithoutCancel(ctx)
	child := exec.CommandContext(detached, exe, a.argv()...) //nolint:gosec // exe is this same binary, args are typed/reconstructed above
	child.Stdout = logFile
	child.Stderr = logFile
	detach(child)
	proc, err := starter.Start(detached, child)
	if err != nil {
		return fmt.Errorf("cmd: run: start background process: %w", err)
	}

	_, _ = fmt.Fprintf(os.Stderr, "started in background (pid %d), logs: %s\n", proc.Pid(), logPath)
	return waitForAddrs(ctx, state, proc, logPath, addrsPollTimeout, addrsPollInterval)
}

// waitForAddrs polls for the run's address file and prints it the same way
// a foreground run would once it appears. It reports an error if proc exits
// first - the caller's exit code is then the only signal a script gets that
// --detach failed to start. Timing out with proc still running is not an
// error: the environment may just be slow to come up, and it keeps running
// in the background regardless; waitForAddrs points at logPath instead.
func waitForAddrs(ctx context.Context, state runstate.State, proc *command.Process, logPath string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if addrs, err := state.ReadAddrs(); err == nil && addrs.ConsoleAddr != "" {
			printEnvironmentInfo(os.Stderr, step.Env{ConsoleAddr: addrs.ConsoleAddr, HTTPProxyAddr: addrs.HTTPProxyAddr})
			return nil
		}
		select {
		case <-proc.Done():
			if err := proc.Wait(); err != nil {
				return fmt.Errorf("cmd: run: background process exited before starting up: %w; see %s", err, logPath)
			}
			return fmt.Errorf("cmd: run: background process exited before starting up; see %s", logPath)
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
	_, _ = fmt.Fprintf(os.Stderr, "still starting; check %s or the console once it's up\n", logPath)
	return nil
}
