package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/session"
)

// groupSep joins a group's own name to a member's bare name for the
// member's engine-internal DAG node name - [config.File]'s own flattening
// convention, matched here exactly since it's also how the engine looks a
// member's outputs back up in a group's deps map.
const groupSep = "."

// memberName builds a group member's engine-internal DAG node name.
func memberName(group, member string) string {
	return group + groupSep + member
}

// resolveNeed resolves one of name's own needs entries to its real DAG
// node name. A group member's needs may name a sibling member by that
// sibling's own bare name (config.Group's own convention: the group joins
// that edge internally, a member never spells out its group's name) -
// resolved here to "<group>.<sibling>". Anything else (a plain step, or a
// group's own bare name) already is its own node name. ok is false when
// dep names neither - config-load validation guarantees this never
// happens for a value that actually reached here, but graph()/downNeeds()
// still need a filter to drop a cross-scope "setup.<name>" entry, resolved
// separately by crossScopeDeps.
func (r *run) resolveNeed(name, dep string) (string, bool) {
	if group, _, ok := strings.Cut(name, groupSep); ok && slices.Contains(r.groups[group].Members, dep) {
		return memberName(group, dep), true
	}
	if _, ok := r.steps[dep]; ok {
		return dep, true
	}
	if _, ok := r.groups[dep]; ok {
		return dep, true
	}
	return "", false
}

// localizeDeps rewrites deps for CEL rendering from a group member's own
// perspective: a sibling member's dag node name ("<group>.<sibling>",
// resolveNeed's own output) is exposed under its own bare name too,
// matching the bare name the member's with block and its own needs list
// both use to refer to it. A non-member name passes through unchanged.
func localizeDeps(name string, deps map[string]dag.Outputs) map[string]dag.Outputs {
	group, _, ok := strings.Cut(name, groupSep)
	if !ok || len(deps) == 0 {
		return deps
	}
	prefix := group + groupSep
	out := make(map[string]dag.Outputs, len(deps))
	for k, v := range deps {
		out[k] = v
		if bare, ok := strings.CutPrefix(k, prefix); ok {
			out[bare] = v
		}
	}
	return out
}

// registerScopeSteps adds every step and group in r's own scope to store,
// each with the display fields its console row needs. A group's own row
// comes from its Group metadata directly, with no plugin to ask about
// kind/icon. A member's display label falls back to its own bare name,
// not its engine-internal "<group>.<member>" one, when it sets none
// itself.
func (r *run) registerScopeSteps(store *session.Store) error {
	// Topological order, not map order: the sidebar and the card grid then
	// read roughly as installation order, a dependency after what it needs.
	for _, name := range r.graph().TopoSort() {
		if grp, ok := r.groups[name]; ok {
			store.AddStep(name, grp.Label, "group", "", nil, grp.Needs, false, "", true)
			// A group is a pure recompute, always safe to sweep into a
			// cascading rerun.
			store.SetStepIdempotent(name, true)
			continue
		}

		step := r.steps[name]
		ref, refErr := config.ParseStepRef(step.Uses)
		if refErr != nil {
			return refErr
		}
		kindLabel := stepKindLabel(stepKind(r.caps[ref.Plugin], ref.Step))

		group, label := "", step.Label
		if g, member, ok := strings.Cut(name, groupSep); ok {
			group = g
			if label == "" {
				label = member
			}
		}

		store.AddStep(name, label, kindLabel, ref.Plugin, r.caps[ref.Plugin].Icon, step.Needs,
			isCompactStep(kindLabel, ref.Plugin, ref.Step), group, false)
		store.SetStepIdempotent(name, stepIdempotent(r.caps[ref.Plugin], ref.Step))
	}
	return nil
}

// upGroup is upStep's branch for a virtual group node: it makes no plugin
// call, it evaluates the group's own outputs block against deps, which
// already carries every member's dag.Outputs.
func (r *run) upGroup(ctx context.Context, name string, grp config.Group, deps map[string]dag.Outputs) (dag.Outputs, error) {
	r.store.SetStep(name, session.Running, "")

	memberOutputs := make(map[string]dag.Outputs, len(grp.Members))
	for _, member := range grp.Members {
		memberOutputs[member] = deps[memberName(name, member)]
	}
	outputs, err := evalGroupOutputs(name, grp.Outputs, memberOutputs)
	if err != nil {
		r.reportUpFailure(ctx, name, err)
		return nil, err
	}

	r.emit(name, "ready")
	r.store.SetStep(name, session.Ready, "")
	return outputs, nil
}

// doExportCrossScopeGroup mirrors doExportCrossScopeStep for a setup-scope
// group: it exports every member (the same live, no-state-file Export call
// doExportCrossScopeStep already makes for one step), then evaluates the
// group's own outputs block against those - the cross-scope counterpart to
// upGroup, which does the same evaluation against a live Up's own deps.
func (r *run) doExportCrossScopeGroup(ctx context.Context, name string, grp config.Group) (dag.Outputs, error) {
	memberOutputs := make(map[string]dag.Outputs, len(grp.Members))
	for _, member := range grp.Members {
		outputs, err := r.doExportCrossScopeStep(ctx, memberName(name, member))
		if err != nil {
			return nil, err
		}
		memberOutputs[member] = outputs
	}
	return evalGroupOutputs(name, grp.Outputs, memberOutputs)
}

// evalGroupOutputs is the pure CEL step both upGroup and
// doExportCrossScopeGroup reduce to: render outputs (a group's own
// "${needs.<member>.out.<key>}" block) against memberOutputs, the same
// expr.Render machinery a step's own with block uses - a group's members
// are simply upstream from the group's own perspective.
func evalGroupOutputs(name string, outputs map[string]string, memberOutputs map[string]dag.Outputs) (dag.Outputs, error) {
	if len(outputs) == 0 {
		return dag.Outputs{}, nil
	}

	raw, err := json.Marshal(outputs)
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}
	rendered, err := expr.Render(raw, name, expr.Scopes{Needs: memberOutputs})
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}
	var result map[string]string
	if err := json.Unmarshal(rendered, &result); err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}

	out := make(dag.Outputs, len(result))
	for k, v := range result {
		out[k] = output.Value{String: v}
	}
	return out, nil
}
