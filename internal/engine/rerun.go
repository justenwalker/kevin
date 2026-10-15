package engine

import (
	"context"
	"fmt"

	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/session"
)

// RerunStep re-executes step name. With cascade false, only name runs -
// every other step keeps its recorded output. With cascade true, name's
// transitive dependents are re-run too: a dependent that never completed
// always joins (it was skipped, nothing of its own to protect); a
// dependent that already completed joins only when its step type is
// idempotent. name itself always runs, regardless of its own idempotent
// flag - it was targeted directly.
func (r *run) RerunStep(ctx context.Context, name string, cascade bool) error {
	if _, ok := r.steps[name]; !ok {
		return fmt.Errorf("%s: %w", name, session.ErrUnknownStep)
	}

	completed := r.state.Snapshot()

	toRun := map[string]bool{name: true}
	if cascade {
		for _, dep := range r.graph().Dependents(name) {
			if _, ok := completed[dep]; !ok {
				toRun[dep] = true
				continue
			}
			if r.idempotent(dep) {
				toRun[dep] = true
			}
		}
	}

	names := make([]string, 0, len(toRun))
	for step := range toRun {
		names = append(names, step)
	}

	results, err := dag.WalkFrom(ctx, r.graph(), toRun, completed, r.upStep, r.cfg.Engine.MaxParallel)
	r.markSkipped(names, results)
	return err
}

// idempotent reports whether step's step type declares itself safe to call
// Up on again. An unparseable step.Uses reports false - the same as any
// step type that never declared IdempotentStep.
func (r *run) idempotent(step string) bool {
	if _, ok := r.groups[step]; ok {
		return true
	}
	ref := r.steps[step].Uses
	return stepIdempotent(r.caps[ref.Plugin], ref.Step)
}

// markSkipped marks every step in considered that dag.Walk (or WalkFrom)
// attempted but has neither a recorded output in results nor already
// reports Failed - a step whose dependency failed, or was itself skipped
// by that cascade.
func (r *run) markSkipped(considered []string, results map[string]output.Map) {
	states := make(map[string]session.State, len(considered))
	for _, s := range r.store.Snapshot().Steps {
		states[s.Name] = s.State
	}
	for _, name := range considered {
		if _, ok := results[name]; ok {
			continue
		}
		if states[name] == session.Failed {
			continue
		}
		r.store.SetStep(name, session.Skipped, "")
	}
}
