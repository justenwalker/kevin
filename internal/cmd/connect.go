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

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
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
		return execStep(ctx, cfg, plugins, caps, env, candidates[0], target)
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

// setupCrossScopeDeps resolves every "setup."-prefixed entry in step's
// needs list via that setup step's Export, keyed by the unprefixed
// name - the shape the "setup" CEL variable uses.
func setupCrossScopeDeps(ctx context.Context, cfg *config.Config, plugins map[string]*pluginhost.Client, caps map[string]pluginhost.Info, env *pb.Environment, step config.Step) (map[string]dag.Outputs, error) {
	var out map[string]dag.Outputs
	for _, dep := range step.Needs {
		setupName, ok := strings.CutPrefix(dep, "setup.")
		if !ok {
			continue
		}
		setupStep, ok := cfg.Setup[setupName]
		if !ok {
			return nil, fmt.Errorf("needs %q: no such step in scope \"setup\"", dep)
		}
		ref, err := config.ParseStepRef(setupStep.Uses)
		if err != nil {
			return nil, err
		}
		client, ok := plugins[ref.Plugin]
		if !ok {
			return nil, fmt.Errorf("plugin %q not loaded", ref.Plugin)
		}
		if !stepExports(caps[ref.Plugin], ref.Step) {
			return nil, fmt.Errorf("setup step %q (%s) does not implement export", setupName, ref)
		}
		with, err := expr.Render(setupStep.With, setupName, expr.Scopes{Project: ca.ProjectVars(cfg.Dir, cfg.Name)})
		if err != nil {
			return nil, fmt.Errorf("setup step %q: %w", setupName, err)
		}
		resp, err := client.Export(ctx, &pb.ExportRequest{
			Step: setupName, Type: ref.Step, Env: env, Config: with,
		})
		if err != nil {
			return nil, fmt.Errorf("export setup step %q: %w", setupName, err)
		}
		if out == nil {
			out = make(map[string]dag.Outputs)
		}
		out[setupName] = outputsFromProto(resp.GetOut())
	}
	return out, nil
}

// outputsFromProto lifts a step's proto outputs into the DAG's Outputs map.
func outputsFromProto(o *pb.Outputs) dag.Outputs {
	values := o.GetValues()
	if len(values) == 0 {
		return nil
	}
	out := make(dag.Outputs, len(values))
	for k, v := range values {
		out[k] = output.Value{String: v.GetStringValue(), Sensitive: v.GetSensitive()}
	}
	return out
}

func execStep(ctx context.Context, cfg *config.Config, plugins map[string]*pluginhost.Client, caps map[string]pluginhost.Info, env *pb.Environment, c candidate, target []string) error {
	client := plugins[c.ref.Plugin]

	setupDeps, err := setupCrossScopeDeps(ctx, cfg, plugins, caps, env, c.step)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	with, err := expr.Render(c.step.With, c.name, expr.Scopes{Setup: setupDeps, Project: ca.ProjectVars(cfg.Dir, cfg.Name)})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}

	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: c.name, Type: c.ref.Step, Env: env, Config: with,
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
