package engine

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cueschema"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
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

	return expr.Scopes{Needs: deps, System: system, Setup: setupDeps, Project: r.project, Vars: r.cfg.VariableValues}
}

// renderWith resolves name's `with` block against deps and the recorded
// system outputs of those deps, plus setupDeps for any cross-scope
// "setup.<name>" needs entry, for either an Up or a Down request.
func (r *run) renderWith(name string, step config.Step, deps, setupDeps map[string]output.Map) (json.RawMessage, error) {
	with, err := expr.Render(step.With, name, r.scopesFor(name, deps, setupDeps))
	if err != nil {
		return nil, fmt.Errorf("%s: with: %w", name, err)
	}
	if err := r.validateRenderedWith(name, step, with); err != nil {
		return nil, err
	}
	return with, nil
}

// validateRenderedWith re-checks with - name's fully rendered with block -
// against its own step type's schema. config-validate time (see
// internal/config's validateStep) already checked every field whose value
// wasn't a bare "${...}" marker, and deferred one that was, since a
// needs/setup-sourced marker's real value doesn't exist until now - this
// catches a deferred field whose rendered value doesn't actually satisfy
// the schema (e.g. a variable declared "type: string" filling a field the
// plugin types "int"). A with block with no marker at all never reaches
// here with anything new to check.
func (r *run) validateRenderedWith(name string, step config.Step, with json.RawMessage) error {
	if !bytes.Contains(step.With, []byte("${")) {
		return nil
	}
	ref := step.Uses
	for _, st := range r.caps[ref.Plugin].Steps {
		if st.Name == ref.Step {
			return config.ValidateAgainstSchema(name+".with", with, st.Schema)
		}
	}
	return nil
}

func depsToProto(deps map[string]output.Map) map[string]*pb.Outputs {
	if len(deps) == 0 {
		return nil
	}
	out := make(map[string]*pb.Outputs, len(deps))
	for name, values := range deps {
		out[name] = values.ToProto()
	}
	return out
}

// stepSchema returns info's step type name's raw schema.cue bytes, or nil
// if name names no step this plugin offers.
func stepSchema(info pluginhost.Info, name string) []byte {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Schema
		}
	}
	return nil
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
