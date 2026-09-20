package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/console"
)

// statusClientTimeout bounds a kevin status request against the running
// console - a wedged console fails fast instead of hanging the command
// (ADR-0004).
const statusClientTimeout = 5 * time.Second

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
			return printStatus(cmd.Context(), cmd.OutOrStdout(), runStateDir(opts.dir, opts.name))
		},
	}
}

// printStatus reports "not running" (mirroring stopRun's own messages)
// without ever making an HTTP request when this project/environment's
// pidfile says nothing is running, and otherwise asks the running
// console's /api/status for current step states.
func printStatus(ctx context.Context, w io.Writer, stateDir string) error {
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

	addrs, err := readRunAddrs(stateDir)
	if err != nil {
		return err
	}
	resp, err := fetchStatus(ctx, addrs.ConsoleAddr)
	if err != nil {
		return err
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

// fetchStatus asks addr's console for its current step states.
func fetchStatus(ctx context.Context, addr string) (console.StatusResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/status", nil)
	if err != nil {
		return console.StatusResponse{}, fmt.Errorf("cmd: status: %w", err)
	}
	client := &http.Client{Timeout: statusClientTimeout}
	res, err := client.Do(req)
	if err != nil {
		return console.StatusResponse{}, fmt.Errorf("cmd: status: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return console.StatusResponse{}, fmt.Errorf("cmd: status: console returned %s", res.Status)
	}

	var out console.StatusResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return console.StatusResponse{}, fmt.Errorf("cmd: status: decode response: %w", err)
	}
	return out, nil
}
