package cmd

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/run/runstate"
	"github.com/justenwalker/kevin/internal/run/steplog"
	"github.com/justenwalker/kevin/internal/statedir"
)

// logsPollInterval is how often --follow re-reads the log file for new
// entries.
var logsPollInterval = 500 * time.Millisecond

func logsCommand(opts *options) *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [step]",
		Short: `Show durable log output for a "kevin run" for this project/environment`,
		Long: "logs prints a step's full recorded output (or, with no step name, " +
			"every step's output interleaved) from this project and environment's " +
			"durable log file. It reads the file directly, so it works whether or " +
			`not "kevin run" is still active, including after a crash.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			step := ""
			if len(args) == 1 {
				step = args[0]
			}
			logsPath := statedir.For(opts.dir, config.SlugName(opts.name)).Logs()
			return printLogs(cmd.Context(), cmd.OutOrStdout(), logsPath, runStateFor(opts.dir, opts.name), step, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, `keep tailing new output until the run stops ("kevin status" reports it not running)`)
	return cmd
}

// printLogs prints step's recorded output (every step's, interleaved, if
// step is ""). With follow, it keeps polling logsPath for new entries
// until ctx is done or the run state says the run has stopped - one
// final read once that happens, not an indefinite tail.
func printLogs(ctx context.Context, w io.Writer, logsPath string, state runstate.State, step string, follow bool) error {
	entries, cursor, err := steplog.ReadSince(logsPath, step, "")
	if err != nil {
		return fmt.Errorf("cmd: logs: %w", err)
	}
	writeLogEntries(w, entries)
	if !follow {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(logsPollInterval):
		}

		entries, cursor, err = steplog.ReadSince(logsPath, step, cursor)
		if err != nil {
			return fmt.Errorf("cmd: logs: %w", err)
		}
		writeLogEntries(w, entries)

		if len(entries) == 0 && !state.Alive() {
			return nil
		}
	}
}

func writeLogEntries(w io.Writer, entries []steplog.Entry) {
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s [%s] %s\n", e.Time.Format("15:04:05"), e.Stream, e.Text)
	}
}
