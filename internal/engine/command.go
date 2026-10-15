package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
)

// CommandPlan is a command from the commands block, resolved and ready to
// run.
type CommandPlan struct {
	// Argv is the command line, with the "${...}" markers of run rendered.
	Argv []string

	// Dir is the working directory to run Argv in.
	Dir string
}

// PlanCommand resolves the named command: it exports every step the command
// needs, the way a step's own cross-scope needs are exported, and renders
// run against what they published. No step comes up.
func PlanCommand(ctx context.Context, opts Options, name string) (CommandPlan, error) {
	h, err := bootstrap(ctx, opts)
	if err != nil {
		return CommandPlan{}, err
	}
	defer h.Close()

	r := &run{cfg: h.Config, runtime: h.Runtime, scope: config.ScopeEnv}
	r.fill(h)
	r.env = environment(h.Config, opts.Engine, config.ScopeEnv, nil)
	if err = h.Configure(ctx, r.env); err != nil {
		return CommandPlan{}, err
	}
	return r.planCommand(ctx, name)
}

func (r *run) planCommand(ctx context.Context, name string) (CommandPlan, error) {
	cmdDef, ok := r.cfg.Commands[name]
	if !ok {
		names := make([]string, 0, len(r.cfg.Commands))
		for n := range r.cfg.Commands {
			names = append(names, n)
		}
		sort.Strings(names)
		return CommandPlan{}, fmt.Errorf("engine: no command named %q, available: %s", name, strings.Join(names, ", "))
	}

	scopes := expr.Scopes{Env: expr.HostEnv(), Project: r.project, Vars: r.cfg.VariableValues}
	for _, need := range cmdDef.Needs {
		scope := config.ScopeEnv
		if need.Setup {
			scope = config.ScopeSetup
		}
		exported, err := r.exportCommandNeed(ctx, scope, need.Step)
		if err != nil {
			return CommandPlan{}, fmt.Errorf("engine: command %q: needs %q: %w", name, need, err)
		}
		if need.Setup {
			if scopes.Setup == nil {
				scopes.Setup = make(map[string]output.Map)
			}
			scopes.Setup[need.Step] = exported.Outputs
			continue
		}
		if scopes.Needs == nil {
			scopes.Needs = make(map[string]output.Map)
		}
		scopes.Needs[need.Step] = exported.Outputs
	}

	tpl, err := expr.Parse(cmdDef.Run)
	if err != nil {
		return CommandPlan{}, fmt.Errorf("engine: command %q: %w", name, err)
	}
	rendered, err := tpl.Render(name, scopes)
	if err != nil {
		return CommandPlan{}, fmt.Errorf("engine: command %q: %w", name, err)
	}
	var argv []string
	if err := json.Unmarshal(rendered, &argv); err != nil {
		return CommandPlan{}, fmt.Errorf("engine: command %q: decode rendered run: %w", name, err)
	}
	return CommandPlan{Argv: argv, Dir: commandDir(r.cfg.Dir, cmdDef.Cwd)}, nil
}

// exportCommandNeed exports one step a command needs. A setup step goes
// through the shared, cached cross-scope export, which also resolves a
// setup group.
func (r *run) exportCommandNeed(ctx context.Context, scope config.Scope, step string) (exportedStep, error) {
	if scope == config.ScopeSetup {
		return r.exportCrossScopeStep(ctx, step)
	}
	return r.doExportStep(ctx, scope, step)
}

// commandDir is a command's working directory: projectDir when cwd is
// empty, otherwise cwd resolved against projectDir if relative.
func commandDir(projectDir, cwd string) string {
	switch {
	case cwd == "":
		return projectDir
	case filepath.IsAbs(cwd):
		return cwd
	default:
		return filepath.Join(projectDir, cwd)
	}
}
