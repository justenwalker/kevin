package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/internal/steplog"
)

// logsPollInterval is how often --follow re-reads the log file for new
// entries.
const logsPollInterval = 500 * time.Millisecond

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
			logsPath := filepath.Join(opts.dir, engine.WorkspaceDir, config.SlugName(opts.name), engine.LogsFile)
			return printLogs(cmd.Context(), cmd.OutOrStdout(), logsPath, runStateDir(opts.dir, opts.name), step, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, `keep tailing new output until the run stops ("kevin status" reports it not running)`)
	return cmd
}

// printLogs prints step's recorded output (every step's, interleaved, if
// step is ""). With follow, it keeps polling logsPath for new entries
// until ctx is done or stateDir's pidfile says the run has stopped - one
// final read once that happens, not an indefinite tail.
func printLogs(ctx context.Context, w io.Writer, logsPath, stateDir, step string, follow bool) error {
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

		if len(entries) == 0 && !runIsAlive(stateDir) {
			return nil
		}
	}
}

// runIsAlive reports whether stateDir's pidfile names a live process -
// pid 0 (no pidfile) is never alive, unlike a bare pidAlive(0) on Unix.
func runIsAlive(stateDir string) bool {
	pid := readPID(stateDir)
	return pid != 0 && pidAlive(pid)
}

func writeLogEntries(w io.Writer, entries []steplog.Entry) {
	for _, e := range entries {
		_, _ = fmt.Fprintf(w, "%s [%s] %s\n", e.Time.Format("15:04:05"), e.Stream, e.Text)
	}
}
