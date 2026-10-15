package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/engine"
)

func doCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "do <name> [-- extra args...]",
		Short: "Run a named command from the commands block",
		Long: "do looks up name in the kevin.cue commands block, exports every " +
			"step its needs list names, renders run's \"${needs...}\"/" +
			"\"${setup...}\" markers against what each step exports, and execs " +
			"the result in place of kevin. Any args after -- append to run.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			name, extra, err := splitDoArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return err
			}
			engineName, err := resolveEngineName(cmd.Context(), opts)
			if err != nil {
				return err
			}
			return runDo(cmd.Context(), opts, engineName, name, extra)
		},
	}
	return cmd
}

// splitDoArgs separates the required command name from the optional
// trailing args, using the position of "--" that cobra records in dash.
func splitDoArgs(args []string, dash int) (string, []string, error) {
	if dash < 0 {
		if len(args) > 1 {
			return "", nil, fmt.Errorf("do: exactly one command name, got %v", args)
		}
		return args[0], nil, nil
	}
	if dash != 1 {
		return "", nil, fmt.Errorf("do: exactly one command name before --, got %v", args[:dash])
	}
	return args[0], args[dash:], nil
}

func runDo(ctx context.Context, opts *options, engineName, cmdName string, extra []string) error {
	plan, err := engine.PlanCommand(ctx, engine.Options{
		Dir:     opts.dir,
		Name:    opts.name,
		Tags:    opts.tags,
		VarFile: opts.varFile,
		Vars:    opts.vars,
		Engine:  engineName,
	}, cmdName)
	if err != nil {
		return err
	}
	if err := os.Chdir(plan.Dir); err != nil {
		return fmt.Errorf("do: %w", err)
	}
	return execWith(append(plan.Argv, extra...))
}
