package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
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
			return runDo(cmd.Context(), opts.dir, opts.name, name, extra)
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

func runDo(ctx context.Context, dir, name, cmdName string, extra []string) error {
	cfg, plugins, caps, err := engine.LoadAndLaunch(ctx, dir, name)
	defer engine.CloseAll(plugins)
	if err != nil {
		return err
	}

	cmdDef, ok := cfg.Commands[cmdName]
	if !ok {
		names := make([]string, 0, len(cfg.Commands))
		for n := range cfg.Commands {
			names = append(names, n)
		}
		sort.Strings(names)
		return fmt.Errorf("do: no command named %q, available: %s", cmdName, strings.Join(names, ", "))
	}

	env := &pb.Environment{
		Project:   cfg.Project,
		Workspace: filepath.Join(cfg.Dir, engine.WorkspaceDir, cfg.Name),
		Network:   engine.NetworkName(cfg.Project),
	}
	if err = engine.ConfigureAll(ctx, cfg.Plugins, plugins, env); err != nil {
		return err
	}

	needsOut, setupOut, err := resolveCommandOutputs(ctx, cfg, plugins, caps, env, cmdDef.Needs)
	if err != nil {
		return err
	}

	rendered, err := expr.Render(cmdDef.Run, cmdName, expr.Scopes{
		Needs: needsOut, Setup: setupOut, Project: ca.ProjectVars(cfg.Dir, cfg.Name),
	})
	if err != nil {
		return fmt.Errorf("do: %w", err)
	}
	var run []string
	if err := json.Unmarshal(rendered, &run); err != nil {
		return fmt.Errorf("do: decode rendered run: %w", err)
	}

	if err := chdirToCwd(cfg.Dir, cmdDef.Cwd); err != nil {
		return err
	}

	argv := make([]string, 0, len(run)+len(extra))
	argv = append(argv, run...)
	argv = append(argv, extra...)
	return execWith(argv)
}

// chdirToCwd changes into a command's working directory: projectDir when
// cwd is empty, otherwise cwd resolved against projectDir if relative.
func chdirToCwd(projectDir, cwd string) error {
	dir := cwd
	switch {
	case dir == "":
		dir = projectDir
	case !filepath.IsAbs(dir):
		dir = filepath.Join(projectDir, dir)
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("do: %w", err)
	}
	return nil
}

// resolveCommandOutputs exports every step needs names, bucketed by scope
// for expr.Render: a plain entry's outputs land under its own name in
// needsOut (read back as "${needs.<step>...}"), a "setup."-prefixed entry's
// under setupOut (read back as "${setup.<name>...}") - the same convention
// a step's own needs list uses. config.File.Validate already proved each of
// these steps implements Export, so this never sees one that doesn't.
func resolveCommandOutputs(ctx context.Context, cfg *config.Config, plugins map[string]*pluginhost.Client, caps map[string]pluginhost.Info, env *pb.Environment, needs []string) (map[string]dag.Outputs, map[string]dag.Outputs, error) {
	var needsOut, setupOut map[string]dag.Outputs
	for _, n := range needs {
		scopeName, steps, stepName := config.ScopeEnv, cfg.Env, n
		if rest, ok := strings.CutPrefix(n, "setup."); ok {
			scopeName, steps, stepName = config.ScopeSetup, cfg.Setup, rest
		}
		step, ok := steps[stepName]
		if !ok {
			return nil, nil, fmt.Errorf("do: needs %q: no such step in scope %q", n, scopeName)
		}
		ref, refErr := config.ParseStepRef(step.Uses)
		if refErr != nil {
			return nil, nil, fmt.Errorf("do: needs %q: %w", n, refErr)
		}
		client, ok := plugins[ref.Plugin]
		if !ok {
			return nil, nil, fmt.Errorf("do: needs %q: plugin %q not loaded", n, ref.Plugin)
		}

		setupDeps, depsErr := setupCrossScopeDeps(ctx, cfg, plugins, caps, env, step)
		if depsErr != nil {
			return nil, nil, fmt.Errorf("do: needs %q: %w", n, depsErr)
		}
		with, renderErr := expr.Render(step.With, stepName, expr.Scopes{Setup: setupDeps, Project: ca.ProjectVars(cfg.Dir, cfg.Name)})
		if renderErr != nil {
			return nil, nil, fmt.Errorf("do: needs %q: %w", n, renderErr)
		}
		resp, exportErr := client.Export(ctx, &pb.ExportRequest{
			Step: stepName, Type: ref.Step, Env: env, Config: with,
		})
		if exportErr != nil {
			return nil, nil, fmt.Errorf("do: needs %q: export: %w", n, exportErr)
		}

		out := outputsFromProto(resp.GetOut())
		if scopeName == config.ScopeSetup {
			if setupOut == nil {
				setupOut = make(map[string]dag.Outputs)
			}
			setupOut[stepName] = out
			continue
		}
		if needsOut == nil {
			needsOut = make(map[string]dag.Outputs)
		}
		needsOut[stepName] = out
	}
	return needsOut, setupOut, nil
}
