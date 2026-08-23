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

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), interruptSignals...)
	rc := run(ctx, os.Args)
	cancel()
	os.Exit(rc)
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
