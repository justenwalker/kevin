package engine

import (
	"context"
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/internal/uerr"
)

func (r *run) down(ctx context.Context) error {
	completed := r.state.Snapshot()
	// An interrupted step gets a Down with no outputs.
	for _, name := range r.state.Interrupted() {
		completed[name] = nil
	}
	if len(completed) == 0 {
		return nil
	}

	// Walk only the steps in completed, in reverse dependency order: a name
	// absent from it never ran, or failed on its own.
	needs := r.downNeeds(completed)

	_, err := dag.Walk(ctx, dag.New(needs).Reverse(), func(ctx context.Context, name string, _ map[string]output.Map) (output.Map, error) {
		return r.downStep(ctx, name, needs[name], completed)
	}, r.cfg.Engine.MaxParallel)
	return err
}

// downNeeds builds down's reduced needs map: only the steps and groups
// that actually came up (are keys in completed), each with only the needs
// edges among those.
func (r *run) downNeeds(completed map[string]output.Map) map[string][]string {
	needs := make(map[string][]string, len(completed))
	for name := range completed {
		var deps []string
		if grp, ok := r.groups[name]; ok {
			for _, member := range grp.Members {
				dotted := config.MemberName(name, member)
				if _, ok := completed[dotted]; ok {
					deps = append(deps, dotted)
				}
			}
		} else {
			for _, dep := range r.steps[name].Needs {
				resolved, ok := r.resolveNeed(name, dep)
				if !ok {
					continue
				}
				if _, ok := completed[resolved]; ok {
					deps = append(deps, resolved)
				}
			}
		}
		needs[name] = deps
	}
	return needs
}

// downStep removes one step or group, the dag.NodeFunc down's reversed
// walk runs per node. deps is name's own downNeeds entry.
func (r *run) downStep(ctx context.Context, name string, deps []string, completed map[string]output.Map) (output.Map, error) {
	if _, ok := r.groups[name]; ok {
		r.store.SetStep(name, session.Removed, "")
		return nil, nil //nolint:nilnil // a nil output.Map is a valid empty result, not the caller ever mistaking it for "not found"
	}

	r.clearFaults(ctx, name)

	cfgStep := r.steps[name]
	ref := cfgStep.Uses
	client := r.plugins[ref.Plugin]

	if !stepImplementsDown(r.caps[ref.Plugin], ref.Step) {
		r.clearRoutes(ctx, name)
		r.store.SetStep(name, session.Removed, "")
		return nil, nil //nolint:nilnil // a nil output.Map is a valid empty result, not the caller ever mistaking it for "not found"
	}

	depOutputs := make(map[string]output.Map, len(deps))
	for _, dep := range deps {
		depOutputs[dep] = completed[dep]
	}
	cross, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return nil, err
	}
	with, renderErr := r.renderWith(name, cfgStep, depOutputs, cross.Outputs)
	if renderErr != nil {
		return nil, renderErr
	}

	estimate, _ := r.timings.EstimateDown(name, ref.String())
	start := time.Now()
	stop := r.trackProgress(ctx, name, estimate)
	defer stop()

	r.emit(name, "down")
	r.store.SetStep(name, session.Removing, "")

	downErr := client.Down(ctx, step.DownRequest{
		Step:    name,
		Type:    ref.Step,
		Env:     r.env,
		Config:  with,
		Outputs: completed[name],
	}, r.onEvent(name))
	if downErr != nil {
		downMsg := uerr.Display(downErr)
		r.emit(name, "teardown failed: "+downMsg)
		r.store.SetStep(name, session.Failed, downMsg)
		return nil, downErr
	}
	r.clearRoutes(ctx, name)
	r.emit(name, "removed")
	r.store.SetStep(name, session.Removed, "")
	r.timings.RecordDown(ctx, name, ref.String(), time.Since(start))
	return nil, nil //nolint:nilnil // a nil output.Map is a valid empty result, not the caller ever mistaking it for "not found"
}
