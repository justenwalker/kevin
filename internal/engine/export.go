package engine

import (
	"context"
	"fmt"
	"maps"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

// Export asks name's plugin how to reach what it created, against this
// session's already-running plugin client.
func (r *run) Export(ctx context.Context, name string) (output.Map, error) {
	step, ok := r.steps[name]
	if !ok {
		return nil, fmt.Errorf("engine: no step named %q", name)
	}
	ref := step.Uses
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return nil, fmt.Errorf("engine: plugin %q not loaded", ref.Plugin)
	}
	setupDeps, _, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return nil, err
	}
	with, err := r.renderWith(name, step, r.sameScopeDeps(name), setupDeps)
	if err != nil {
		return nil, err
	}
	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: name, Type: ref.Step, Env: r.env, Config: with,
	})
	if err != nil {
		return nil, fmt.Errorf("engine: export %s: %w", name, err)
	}
	return output.FromProto(resp.GetOut()), nil
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

// exportedStep is what exportCrossScopeStep resolves for one setup-scope
// step: its Outputs (the "setup" CEL variable and cross-scope Deps use
// this) and its Containers (UpRequest.Containers uses this) - one Export
// call answers both.
type exportedStep struct {
	Outputs    output.Map
	Containers []*pb.ContainerInfo
}

// exportCrossScopeStep asks a setup-scope step's plugin how to reach what
// it created - the same request Export makes against this session's
// already-running plugin, sent with the setup step's own unrendered with
// block, the same as Export. setupName is the name
// with the "setup." prefix already stripped.
func (r *run) exportCrossScopeStep(ctx context.Context, setupName string) (exportedStep, error) {
	v, err, _ := r.exportGroup.Do(setupName, func() (any, error) {
		if grp, ok := r.cfg.Groups(config.ScopeSetup)[setupName]; ok {
			return r.doExportCrossScopeGroup(ctx, setupName, grp)
		}
		return r.doExportCrossScopeStep(ctx, setupName)
	})
	if err != nil {
		return exportedStep{}, err //nolint:wrapcheck // doExportCrossScopeStep already wraps its own errors; singleflight only relays them
	}
	exported, _ := v.(exportedStep) // this Group's Do calls only ever return exportedStep
	return exported, nil
}

// doExportCrossScopeStep is exportCrossScopeStep's uncached call.
func (r *run) doExportCrossScopeStep(ctx context.Context, setupName string) (exportedStep, error) {
	return r.doExportStep(ctx, config.ScopeSetup, setupName)
}

// doExportStep asks the plugin of the named step of scope how to reach what
// the step created. An env step's own setup needs are exported first, so its
// with block renders.
func (r *run) doExportStep(ctx context.Context, scope config.Scope, name string) (exportedStep, error) {
	step, ok := r.cfg.Steps(scope)[name]
	if !ok {
		return exportedStep{}, fmt.Errorf("no such step in scope %q", scope)
	}
	ref := step.Uses
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return exportedStep{}, fmt.Errorf("plugin %q not loaded", ref.Plugin)
	}
	if !stepExports(r.caps[ref.Plugin], ref.Step) {
		return exportedStep{}, fmt.Errorf("%s step %q (%s) does not implement export", scope, name, ref)
	}
	var setupDeps map[string]output.Map
	if scope == config.ScopeEnv {
		var err error
		if setupDeps, _, err = r.crossScopeDeps(ctx, name); err != nil {
			return exportedStep{}, err
		}
	}
	with, err := expr.Render(step.With, name, expr.Scopes{Setup: setupDeps, Project: r.project, Vars: r.cfg.VariableValues})
	if err != nil {
		return exportedStep{}, fmt.Errorf("%s step %q: %w", scope, name, err)
	}
	if validateErr := r.validateRenderedWith(name, step, with); validateErr != nil {
		return exportedStep{}, validateErr
	}
	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: name, Type: ref.Step, Env: r.env, Config: with,
	})
	if err != nil {
		return exportedStep{}, fmt.Errorf("export %s step %q: %w", scope, name, err)
	}
	return exportedStep{Outputs: output.FromProto(resp.GetOut()), Containers: resp.GetContainers()}, nil
}

// crossScopeDeps resolves every "setup."-prefixed entry in name's own
// needs list via Export, keyed by the unprefixed setup-step name - the
// shape both the "setup" CEL variable and the step's wire Deps use. The
// second return value carries the same steps' Containers, for
// UpRequest.Containers. A name with no such entry returns (nil, nil,
// nil) - the common case, most steps use only same-scope needs.
func (r *run) crossScopeDeps(ctx context.Context, name string) (map[string]output.Map, map[string][]*pb.ContainerInfo, error) {
	var out map[string]output.Map
	var containers map[string][]*pb.ContainerInfo
	for _, dep := range r.steps[name].Needs {
		if !dep.Setup {
			continue // same-scope, resolved by the dag walk already.
		}
		setupName := dep.Step
		exported, err := r.exportCrossScopeStep(ctx, setupName)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: needs %q: %w", name, dep, err)
		}
		if out == nil {
			out = make(map[string]output.Map)
		}
		out[setupName] = exported.Outputs
		if len(exported.Containers) > 0 {
			if containers == nil {
				containers = make(map[string][]*pb.ContainerInfo)
			}
			containers[setupName] = exported.Containers
		}
	}
	return out, containers, nil
}

// sameScopeDeps builds name's same-scope deps map from already-completed
// step outputs. Use it outside the DAG walk, such as from Export.
func (r *run) sameScopeDeps(name string) map[string]output.Map {
	completed := r.state.Snapshot()
	var out map[string]output.Map
	for _, dep := range r.steps[name].Needs {
		if dep.Setup {
			continue // cross-scope, resolved by crossScopeDeps instead.
		}
		resolved, ok := r.resolveNeed(name, dep)
		if !ok {
			continue
		}
		outputs, ok := completed[resolved]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]output.Map)
		}
		out[resolved] = outputs
	}
	return out
}

// setupKey is the key a setup step's value carries on the wire, where it
// shares a map with same-scope steps.
func setupKey(name string) string {
	return config.Need{Setup: true, Step: name}.String()
}

// depsWithSetup folds setupDeps into deps for the wire Deps field, each
// entry keyed "setup.<name>". This is the one channel that still carries
// a value's Sensitive flag once it crosses scopes.
func depsWithSetup(deps, setupDeps map[string]output.Map) map[string]output.Map {
	if len(setupDeps) == 0 {
		return deps
	}
	out := make(map[string]output.Map, len(deps)+len(setupDeps))
	maps.Copy(out, deps)
	for name, vals := range setupDeps {
		out[setupKey(name)] = vals
	}
	return out
}

// stepContainersFor builds the UpRequest.Containers a step's own needs
// resolve to: deps' keys name its same-scope needs, looked up in
// r.state.containers; setupContainers is what crossScopeDeps already
// resolved for its "setup."-prefixed ones, reattaching that prefix here -
// the same shape depsWithSetup gives the wire Deps field. A needs entry
// that reported no containers (most step types) contributes no entry, not
// an empty one.
func (r *run) stepContainersFor(deps map[string]output.Map, setupContainers map[string][]*pb.ContainerInfo) []*pb.StepContainers {
	var out []*pb.StepContainers
	for name := range deps {
		if containers := r.state.Containers(name); len(containers) > 0 {
			out = append(out, &pb.StepContainers{Step: name, Containers: containers})
		}
	}
	for name, containers := range setupContainers {
		out = append(out, &pb.StepContainers{Step: setupKey(name), Containers: containers})
	}
	return out
}
