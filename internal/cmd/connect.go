package cmd

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

func connectCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect [step] [-- command...]",
		Short: "Export a step's environment and run a command",
		Long: "connect asks a step how to reach what it created (a builtin:kind " +
			"step reports KUBECONFIG, for example), exports that environment, and " +
			"execs the given command in place of kevin. With no command, connect " +
			"starts $SHELL. With no step named and more than one step supports " +
			"connect, connect lists them and asks you to name one.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			step, target, err := splitConnectArgs(args, cmd.ArgsLenAtDash())
			if err != nil {
				return err
			}
			return runConnect(cmd.Context(), opts.dir, opts.name, step, target)
		},
	}
	return cmd
}

// splitConnectArgs separates the optional step name from the target
// command, using the position of "--" that cobra records in dash. At most
// one arg may precede the target command, or exist at all with no "--".
func splitConnectArgs(args []string, dash int) (string, []string, error) {
	if dash < 0 {
		if len(args) > 1 {
			return "", nil, fmt.Errorf("connect: at most one step name, got %v", args)
		}
		var step string
		if len(args) == 1 {
			step = args[0]
		}
		return step, nil, nil
	}
	if dash > 1 {
		return "", nil, fmt.Errorf("connect: at most one step name before --, got %v", args[:dash])
	}
	var step string
	if dash == 1 {
		step = args[0]
	}
	return step, args[dash:], nil
}

// candidateSteps returns the steps that connect should consider: every step
// of both scopes, or the one step named name. candidateSteps returns an
// error when name names no step at all.
func candidateSteps(cfg *config.Config, name string) (map[string]config.Step, error) {
	steps := make(map[string]config.Step, len(cfg.Env)+len(cfg.Setup))
	maps.Copy(steps, cfg.Env)
	maps.Copy(steps, cfg.Setup)

	if name == "" {
		return steps, nil
	}
	s, ok := steps[name]
	if !ok {
		return nil, fmt.Errorf("connect: no step named %q in the kevin environment file", name)
	}
	return map[string]config.Step{name: s}, nil
}

func runConnect(ctx context.Context, dir, name, step string, target []string) error {
	cfg, plugins, caps, err := engine.LoadAndLaunch(ctx, dir, name)
	defer engine.CloseAll(plugins)
	if err != nil {
		return err
	}

	env := &pb.Environment{
		Project:   cfg.Project,
		Workspace: filepath.Join(cfg.Dir, engine.WorkspaceDir, cfg.Name),
		Network:   engine.NetworkName(cfg.Project),
	}
	if err = engine.ConfigureAll(ctx, cfg.Plugins, plugins, env); err != nil {
		return err
	}

	steps, err := candidateSteps(cfg, step)
	if err != nil {
		return err
	}

	candidates, err := connectableSteps(caps, steps)
	if err != nil {
		return err
	}

	switch len(candidates) {
	case 0:
		if step != "" {
			return fmt.Errorf("connect: step %q does not support connect", step)
		}
		return errors.New("connect: no step in the kevin environment file supports connect")
	case 1:
		return execStep(ctx, plugins, env, candidates[0], target)
	default:
		names := make([]string, len(candidates))
		for i, c := range candidates {
			names[i] = c.name
		}
		return fmt.Errorf("connect: more than one step supports connect (%s), name one: kevin connect <step>", strings.Join(names, ", "))
	}
}

type candidate struct {
	name string
	ref  config.StepRef
	step config.Step
}

// connectableSteps keeps only the steps whose plugin explicitly reports, via
// Info, that the step's type implements Export.
func connectableSteps(caps map[string]pluginhost.Info, steps map[string]config.Step) ([]candidate, error) {
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []candidate
	for _, name := range names {
		s := steps[name]
		ref, err := config.ParseStepRef(s.Uses)
		if err != nil {
			return nil, err
		}
		if stepExports(caps[ref.Plugin], ref.Step) {
			out = append(out, candidate{name: name, ref: ref, step: s})
		}
	}
	return out, nil
}

// stepExports reports whether info's step type name implements Export.
func stepExports(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Export
		}
	}
	return false
}

func execStep(ctx context.Context, plugins map[string]*pluginhost.Client, env *pb.Environment, c candidate, target []string) error {
	client := plugins[c.ref.Plugin]
	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: c.name, Type: c.ref.Step, Env: env, Config: c.step.With,
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	return execWith(resp.GetEnv(), target)
}

// execWith execs target with vars added to the environment, replacing the
// kevin process. With no target, execWith starts $SHELL, falling back to
// /bin/sh.
func execWith(vars map[string]string, target []string) error {
	if len(target) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		target = []string{shell}
	}
	bin, err := exec.LookPath(target[0])
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	env := os.Environ()
	for k, v := range vars {
		env = append(env, k+"="+v)
	}
	if err := syscall.Exec(bin, target, env); err != nil { //nolint:gosec // the command is what the user asked to run
		return fmt.Errorf("connect: exec %s: %w", bin, err)
	}
	return nil
}
