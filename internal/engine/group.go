package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/session"
)

// resolveNeed resolves one of name's own needs entries to its DAG node name,
// or reports false for a cross-scope "setup." entry, which crossScopeDeps
// resolves separately.
func (r *run) resolveNeed(name string, dep config.Need) (string, bool) {
	return config.ResolveNeed(name, dep, r.steps, r.groups)
}

// localizeDeps rewrites deps for CEL rendering from a group member's own
// perspective: a sibling member's dag node name ("<group>.<sibling>",
// resolveNeed's own output) is exposed under its own bare name too,
// matching the bare name the member's with block and its own needs list
// both use to refer to it. A non-member name passes through unchanged.
func localizeDeps(name string, deps map[string]output.Map) map[string]output.Map {
	group, _, ok := strings.Cut(name, config.GroupMemberSep)
	if !ok || len(deps) == 0 {
		return deps
	}
	prefix := group + config.GroupMemberSep
	out := make(map[string]output.Map, len(deps))
	for k, v := range deps {
		out[k] = v
		if bare, ok := strings.CutPrefix(k, prefix); ok {
			out[bare] = v
		}
	}
	return out
}

// subtractNeeds returns needs with every entry in exclude removed,
// preserving order.
func subtractNeeds(needs, exclude []config.Need) []config.Need {
	if len(exclude) == 0 {
		return needs
	}
	skip := make(map[config.Need]bool, len(exclude))
	for _, n := range exclude {
		skip[n] = true
	}
	out := make([]config.Need, 0, len(needs))
	for _, n := range needs {
		if !skip[n] {
			out = append(out, n)
		}
	}
	return out
}

// hasStep reports whether name is a step of the run, a group member included.
func (r *run) hasStep(name string) bool {
	_, ok := r.steps[name]
	return ok
}

// qualifySiblings returns needs with each bare sibling member name replaced
// by its "<group>.<member>" step name, the one a console row is keyed by.
func (r *run) qualifySiblings(group string, needs []config.Need) []config.Need {
	out := make([]config.Need, len(needs))
	for i, n := range needs {
		out[i] = n
		if n.Setup {
			continue
		}
		if qualified := config.MemberName(group, n.Step); r.hasStep(qualified) {
			out[i] = config.Need{Step: qualified}
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
			store.AddStep(session.Step{Name: name, Label: grp.Label, Kind: "group", Needs: config.NeedStrings(grp.Needs), IsGroup: true})
			// A group is a pure recompute, always safe to sweep into a
			// cascading rerun.
			store.SetStepIdempotent(name, true)
			continue
		}

		step := r.steps[name]
		ref := step.Uses
		kindLabel := stepKind(r.caps[ref.Plugin], ref.Step).String()

		group, label := "", step.Label
		if g, member, ok := strings.Cut(name, config.GroupMemberSep); ok {
			group = g
			if label == "" {
				label = member
			}
		}

		needs := step.Needs
		if group != "" {
			// A member's needs always includes its group's own shared needs
			// (config/group.go's unionNeeds) - drop only those, since the
			// group's own row already draws that edge. What's left (a
			// sibling reference, or a need unique to this member, e.g. its
			// own "setup.<name>") is real information the group's row
			// doesn't carry, so it still reaches the console.
			needs = subtractNeeds(needs, r.groups[group].Needs)
			needs = r.qualifySiblings(group, needs)
		}
		store.AddStep(session.Step{
			Name: name, Label: label, Kind: kindLabel, Provider: ref.Plugin,
			Icon: r.caps[ref.Plugin].Icon, Needs: config.NeedStrings(needs),
			Compact: isCompactStep(kindLabel, ref.Plugin, ref.Step), Group: group,
		})
		store.SetStepIdempotent(name, stepIdempotent(r.caps[ref.Plugin], ref.Step))
	}
	return nil
}

// upGroup is upStep's branch for a virtual group node: it makes no plugin
// call, it evaluates the group's own outputs block against deps, which
// already carries every member's output.Map.
func (r *run) upGroup(ctx context.Context, name string, grp config.Group, deps map[string]output.Map) (output.Map, error) {
	r.store.SetStep(name, session.Running, "")

	memberOutputs := make(map[string]output.Map, len(grp.Members))
	for _, member := range grp.Members {
		memberOutputs[member] = deps[config.MemberName(name, member)]
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
func (r *run) doExportCrossScopeGroup(ctx context.Context, name string, grp config.Group) (exportedStep, error) {
	memberOutputs := make(map[string]output.Map, len(grp.Members))
	for _, member := range grp.Members {
		exported, err := r.doExportCrossScopeStep(ctx, config.MemberName(name, member))
		if err != nil {
			return exportedStep{}, err
		}
		memberOutputs[member] = exported.Outputs
	}
	outputs, err := evalGroupOutputs(name, grp.Outputs, memberOutputs)
	if err != nil {
		return exportedStep{}, err
	}
	// A group has no containers of its own - it's a CEL-evaluated view over
	// its members' outputs, not a real plugin step with its own workload.
	return exportedStep{Outputs: outputs}, nil
}

// evalGroupOutputs is the pure CEL step both upGroup and
// doExportCrossScopeGroup reduce to: render outputs (a group's own
// "${needs.<member>.out.<key>}" block) against memberOutputs, the same
// expr.Template machinery a step's own with block uses - a group's members
// are simply upstream from the group's own perspective.
func evalGroupOutputs(name string, outputs map[string]string, memberOutputs map[string]output.Map) (output.Map, error) {
	if len(outputs) == 0 {
		return output.Map{}, nil
	}

	raw, err := json.Marshal(outputs)
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}
	tpl, err := expr.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}
	scopes := expr.Scopes{Env: expr.HostEnv(), Needs: memberOutputs}
	rendered, err := tpl.Render(name, scopes)
	if err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}
	var result map[string]string
	if err := json.Unmarshal(rendered, &result); err != nil {
		return nil, fmt.Errorf("%s: outputs: %w", name, err)
	}

	out := make(output.Map, len(result))
	for k, v := range result {
		field, err := json.Marshal(outputs[k])
		if err != nil {
			return nil, fmt.Errorf("%s: outputs: %s: %w", name, k, err)
		}
		fieldTpl, err := expr.Parse(field)
		if err != nil {
			return nil, fmt.Errorf("%s: outputs: %s: %w", name, k, err)
		}
		out[k] = output.Value{String: v, Sensitive: fieldTpl.Sensitive(scopes, nil)}
	}
	return out, nil
}
