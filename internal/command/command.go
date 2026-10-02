// Package command runs host commands to completion. [Runner] is the
// contract that anything which shells out takes, so a test can stand in for
// the real process.
package command

import (
	"context"
	"os/exec"
)

// Runner runs cmd to completion. cmd is built with [exec.CommandContext]; its
// Path, Args, Env, Dir, and standard streams are honoured, and ctx cancels
// the process.
type Runner interface {
	Run(ctx context.Context, cmd *exec.Cmd) error
}

// Default is the [Runner] behind [Run]: it starts a real process.
var Default Runner = execRunner{}

// Run runs cmd with [Default].
func Run(ctx context.Context, cmd *exec.Cmd) error {
	return Default.Run(ctx, cmd)
}

// execRunner starts real processes. It is safe for concurrent use.
type execRunner struct{}

// Run copies cmd onto an [exec.CommandContext] and runs that, because a
// built [exec.Cmd] can't be given a context after the fact.
func (execRunner) Run(ctx context.Context, cmd *exec.Cmd) error {
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
	return c.Run() //nolint:wrapcheck // callers inspect the process's own exit error
}
