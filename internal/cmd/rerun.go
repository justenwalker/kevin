package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/run/api"
	"github.com/justenwalker/kevin/internal/run/runstate"
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
			return rerunStep(cmd.Context(), cmd.OutOrStdout(), runStateFor(opts.dir, opts.name), args[0], cascade)
		},
	}
	cmd.Flags().BoolVar(&cascade, "cascade", false, "also rerun the steps that depend on the step")
	return cmd
}

// rerunStep reruns step on the running console and prints the state of every
// step the rerun changed. The request has no timeout: it is bound to ctx, so
// an interrupt cancels the rerun the way closing a console tab does.
func rerunStep(ctx context.Context, w io.Writer, state runstate.State, step string, cascade bool) error {
	addrs, err := state.Running()
	if err != nil {
		return err
	}
	result, err := api.Dial(addrs).Rerun(ctx, api.RerunRequest{Step: step, Cascade: cascade})
	switch {
	case errors.Is(err, api.ErrUnknownStep):
		return fmt.Errorf("cmd: rerun: no step named %q", step)
	case errors.Is(err, api.ErrBusy):
		return fmt.Errorf("cmd: rerun: step %q is busy", step)
	case err != nil:
		return fmt.Errorf("cmd: rerun: %w", err)
	}
	return printRerun(w, result)
}

// printRerun prints each step the rerun changed, and fails if the target
// step ended failed.
func printRerun(w io.Writer, result api.RerunResult) error {
	for _, st := range result.Changed {
		line := st.Name + " " + st.State
		if st.Message != "" {
			line += ": " + st.Message
		}
		_, _ = fmt.Fprintln(w, line)
	}
	if result.Failed {
		return fmt.Errorf("%w: %s", ErrRerunFailed, result.Step)
	}
	return nil
}
