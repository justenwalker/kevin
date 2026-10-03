package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/console"
	"github.com/justenwalker/kevin/internal/session"
)

// ErrRerunFailed reports that a rerun finished with the target step failed.
const ErrRerunFailed = Error("cmd: rerun: step failed")

func rerunCommand(opts *options) *cobra.Command {
	var cascade bool

	cmd := &cobra.Command{
		Use:   "rerun <step>",
		Short: `Rerun a step of a "kevin run" for this project/environment`,
		Long: `rerun re-executes one step of a running "kevin run" for this project and ` +
			"environment, the same as the console's rerun button. It returns once the rerun " +
			"finishes and exits nonzero if the step failed.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			return rerunStep(cmd.Context(), cmd.OutOrStdout(), runStateDir(opts.dir, opts.name), args[0], cascade)
		},
	}
	cmd.Flags().BoolVar(&cascade, "cascade", false, "also rerun the steps that depend on the step")
	return cmd
}

// rerunStep posts a rerun of step to the running console and prints the
// state of every step the rerun changed. The request has no timeout: it is
// bound to ctx, so an interrupt cancels the rerun the way closing a console
// tab does.
func rerunStep(ctx context.Context, w io.Writer, stateDir, step string, cascade bool) error {
	addrs, err := runningConsole(stateDir)
	if err != nil {
		return err
	}
	before, err := fetchStatus(ctx, addrs.ConsoleAddr)
	if err != nil {
		return err
	}

	form := url.Values{"cascade": {strconv.FormatBool(cascade)}}
	endpoint := "http://" + addrs.ConsoleAddr + "/steps/" + url.PathEscape(step) + "/rerun"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("cmd: rerun: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cmd: rerun: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	switch res.StatusCode {
	case http.StatusAccepted:
	case http.StatusNotFound:
		return fmt.Errorf("cmd: rerun: no step named %q", step)
	case http.StatusConflict:
		return fmt.Errorf("cmd: rerun: step %q is busy", step)
	default:
		return fmt.Errorf("cmd: rerun: console returned %s", res.Status)
	}

	after, err := fetchStatus(ctx, addrs.ConsoleAddr)
	if err != nil {
		return err
	}
	return printRerun(w, step, before, after)
}

// printRerun prints step and each other step whose state or message differs
// between before and after, and fails if step itself ended Failed.
func printRerun(w io.Writer, step string, before, after console.StatusResponse) error {
	prev := make(map[string]string, len(before.Steps))
	for _, st := range before.Steps {
		prev[st.Name] = st.State + "\x00" + st.Message
	}
	var failed bool
	for _, st := range after.Steps {
		if st.Name == step && st.State == string(session.Failed) {
			failed = true
		}
		if st.Name != step && prev[st.Name] == st.State+"\x00"+st.Message {
			continue
		}
		line := st.Name + " " + st.State
		if st.Message != "" {
			line += ": " + st.Message
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if failed {
		return fmt.Errorf("%w: %s", ErrRerunFailed, step)
	}
	return nil
}
