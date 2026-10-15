package engine

import (
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cueschema"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/plugins/launcher"
)

// scopesFor builds the expr.Scopes name's `with` block renders against:
// deps' own outputs plus the recorded system outputs of those deps, and
// setupDeps for any cross-scope "setup.<name>" needs entry.
func (r *run) scopesFor(name string, deps, setupDeps map[string]output.Map) expr.Scopes {
	system := r.state.System(deps)

	// A group member's own needs/with block names a sibling by that
	// sibling's bare name (graph() joins the DAG edge to its
	// "<group>.<sibling>" node internally) - expose it under that bare
	// name here too, so needs.<sibling>... resolves the way the member
	// actually wrote it.
	deps = localizeDeps(name, deps)
	system = localizeDeps(name, system)

	return expr.Scopes{Needs: deps, System: system, Setup: setupDeps, Env: expr.HostEnv(), Project: r.project, Vars: r.cfg.VariableValues}
}

// renderWith resolves name's `with` block against deps and the recorded
// system outputs of those deps, plus setupDeps for any cross-scope
// "setup.<name>" needs entry, for either an Up or a Down request. The result
// carries every default its step type's schema declares.
func (r *run) renderWith(name string, step config.Step, deps, setupDeps map[string]output.Map) (json.RawMessage, error) {
	tpl, err := step.WithTemplate()
	if err != nil {
		return nil, fmt.Errorf("%s: with: %w", name, err)
	}
	with, err := tpl.Render(name, r.scopesFor(name, deps, setupDeps))
	if err != nil {
		return nil, fmt.Errorf("%s: with: %w", name, err)
	}
	return r.applySchema(name, step, with)
}

// applySchema checks with - name's fully rendered with block - against its
// own step type's schema, and returns it with the schema's defaults filled
// in, so a plugin never has to repeat them. config-validate time (see
// internal/config's validateStep) already checked every field whose value
// wasn't a bare "${...}" marker, and deferred one that was, since a
// needs/setup-sourced marker's real value doesn't exist until now - this
// catches a deferred field whose rendered value doesn't actually satisfy
// the schema (e.g. a variable declared "type: string" filling a field the
// plugin types "int").
func (r *run) applySchema(name string, step config.Step, with json.RawMessage) (json.RawMessage, error) {
	ref := step.Uses
	st, ok := r.caps[ref.Plugin].Step(ref.Step)
	if !ok {
		return with, nil
	}
	return config.ApplySchema(name+".with", with, st.Schema)
}

// stepSchema returns info's step type name's raw schema.cue bytes, or nil
// if name names no step this plugin offers.
func stepSchema(info launcher.Info, name string) []byte {
	st, _ := info.Step(name)
	return st.Schema
}

// sensitiveWithFields reports which of schema's #Config fields carry a
// "@sensitive()" attribute, keyed by field name.
func sensitiveWithFields(schema []byte) (map[string]bool, error) {
	if len(schema) == 0 {
		return nil, nil //nolint:nilnil // no schema means no sensitive fields, a valid empty result
	}
	s, err := cueschema.ParseSource("schema.cue", schema)
	if err != nil {
		return nil, fmt.Errorf("with: parse schema: %w", err)
	}
	fields := s.Definitions["Config"].Fields
	sensitive := make(map[string]bool, len(fields))
	for _, f := range fields {
		if f.Sensitive {
			sensitive[f.Name] = true
		}
	}
	return sensitive, nil
}
