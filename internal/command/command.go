// Package command runs host commands. [Runner] and [Starter] are the
// contracts that anything which shells out takes, so a test can stand in for
// the real process.
package command

import (
	"context"
	"os/exec"
	"sync"
)

// Runner runs cmd to completion. cmd is built with [exec.CommandContext]; its
// Path, Args, Env, Dir, and standard streams are honoured, and ctx cancels
// the process.
type Runner interface {
	Run(ctx context.Context, cmd *exec.Cmd) error
}

// Starter starts cmd without waiting for it to exit, and returns the running
// [Process]. It takes the same cmd as [Runner].
type Starter interface {
	Start(ctx context.Context, cmd *exec.Cmd) (*Process, error)
}

// Process is a started command. Wait and Done may be used from any goroutine.
type Process struct {
	pid  int
	wait func() error
	once sync.Once
	done chan struct{}
	err  error
}

// NewProcess builds a Process for pid whose exit is reported by wait. wait
// runs once, on a goroutine NewProcess starts, so an exited child is reaped
// instead of lingering as a zombie that kill(pid, 0) still sees. wait must
// eventually return, or that goroutine stays blocked.
func NewProcess(pid int, wait func() error) *Process {
	p := &Process{pid: pid, wait: wait, done: make(chan struct{})}
	go func() { _ = p.Wait() }()
	return p
}

// Pid returns the process id.
func (p *Process) Pid() int { return p.pid }

// Wait blocks until the process exits and returns its exit error. Every call
// returns the same result.
func (p *Process) Wait() error {
	p.once.Do(func() {
		p.err = p.wait()
		close(p.done)
	})
	return p.err
}

// Done is closed once the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Default is the [Runner] behind [Run]: it starts a real process.
var Default Runner = execRunner{}

// DefaultStarter is the [Starter] behind [Start]: it starts a real process.
var DefaultStarter Starter = execRunner{}

// Run runs cmd with [Default].
func Run(ctx context.Context, cmd *exec.Cmd) error {
	return Default.Run(ctx, cmd)
}

// Start starts cmd with [DefaultStarter].
func Start(ctx context.Context, cmd *exec.Cmd) (*Process, error) {
	return DefaultStarter.Start(ctx, cmd)
}

// execRunner starts real processes. It is safe for concurrent use.
type execRunner struct{}

// Run copies cmd onto an [exec.CommandContext] and runs that, because a
// built [exec.Cmd] can't be given a context after the fact.
func (execRunner) Run(ctx context.Context, cmd *exec.Cmd) error {
	return copyOf(ctx, cmd).Run() //nolint:wrapcheck // callers inspect the process's own exit error
}

// Start copies cmd the same way Run does and starts the copy.
func (execRunner) Start(ctx context.Context, cmd *exec.Cmd) (*Process, error) {
	c := copyOf(ctx, cmd)
	if err := c.Start(); err != nil {
		return nil, err //nolint:wrapcheck // callers inspect the start error itself
	}
	return NewProcess(c.Process.Pid, c.Wait), nil
}

// copyOf builds an [exec.CommandContext] that carries cmd's settings.
func copyOf(ctx context.Context, cmd *exec.Cmd) *exec.Cmd {
	//nolint:gosec // cmd is built by the caller, which owns its arguments
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Args = cmd.Args
	c.Env = cmd.Env
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.ExtraFiles = cmd.ExtraFiles
	c.SysProcAttr = cmd.SysProcAttr
	c.WaitDelay = cmd.WaitDelay
	return c
}
