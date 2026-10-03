// Command kevin runs a local dev environment.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/uerr"
)

// forceQuitCode is the exit status of a run ended by a second interrupt,
// the shell convention for death by SIGINT.
const forceQuitCode = 130

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	watchInterrupts(cancel, os.Exit)
	rc := run(ctx, os.Args)
	cancel()
	os.Exit(rc)
}

// watchInterrupts cancels the run on the first interrupt and calls exit on
// the second, so a stuck shutdown can be abandoned.
func watchInterrupts(cancel context.CancelFunc, exit func(int)) {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, interruptSignals...)
	go func() {
		<-sigs
		errPrintln("interrupted, shutting down; interrupt or signal again to force quit")
		cancel()
		<-sigs
		errPrintln("forced quit; run kevin again to clean up what is left")
		exit(forceQuitCode)
	}()
}

func run(ctx context.Context, args []string) int {
	err := cmd.Run(ctx, args[1:])
	if err == nil {
		return 0
	}

	errPrintln("ERROR:", uerr.Display(err))

	if cmdErr, ok := errors.AsType[*cmd.CommandError](err); ok && cmdErr.Cmd != nil {
		errPrintln()
		errPrintln(cmdErr.Cmd.UsageString())
		return 2
	}
	return 1
}

func errPrintln(v ...any) {
	_, _ = fmt.Fprintln(os.Stderr, v...)
}
