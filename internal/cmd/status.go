package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/runstate"
)

func statusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: `Show step status for a "kevin run" for this project/environment`,
		Long: `status reports every step's current state for a "kevin run" ` +
			"(started plain or with --detach) for this project and environment - " +
			"the same data the web console shows, without opening a browser.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			return printStatus(cmd.Context(), cmd.OutOrStdout(), runStateFor(opts.dir, opts.name))
		},
	}
}

// printStatus reports "not running" (mirroring runstate.State.Stop's own messages)
// without ever making an HTTP request when this project/environment's
// pidfile says nothing is running, and otherwise asks the running
// console's /api/status for current step states.
func printStatus(ctx context.Context, w io.Writer, state runstate.State) error {
	addrs, err := state.Running()
	if errors.Is(err, runstate.ErrNotRunning) {
		_, _ = fmt.Fprintln(w, "not running")
		return nil
	}
	if errors.Is(err, runstate.ErrStaleRun) {
		_, _ = fmt.Fprintln(w, "not running (removed stale pid file)")
		return nil
	}
	if err != nil {
		return err
	}
	resp, err := runapi.Dial(addrs).Status(ctx)
	if err != nil {
		return fmt.Errorf("cmd: status: %w", err)
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tMESSAGE")
	for _, st := range resp.Steps {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", st.Name, st.State, st.Message)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("cmd: status: %w", err)
	}
	return nil
}
