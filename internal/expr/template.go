package expr

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/google/cel-go/cel"
)

// Template is a JSON value whose "${...}" markers are parsed. Parse a with or
// config block once; its references and sensitivity are then read without
// parsing it again.
//
// A Template is immutable after [Parse], so any number of goroutines may call
// its methods at once.
type Template struct {
	raw     json.RawMessage
	refs    Refs
	keys    []keyRef
	fields  map[string]fieldRefs
	hasMark bool
}

// fieldRefs is the part of a [Template]'s markers that one top-level field of
// an object holds.
type fieldRefs struct {
	vars []string
	keys []keyRef
}

// Refs is every scope reference a [Template]'s markers make, by name.
type Refs struct {
	// Needs names each step read as "needs.<step>...".
	Needs []string
	// Setup names each setup step read as "setup.<step>...".
	Setup []string
	// Vars names each variable read as "vars.<name>".
	Vars []string
}

// Parse parses the markers in raw, with no evaluation. raw with no marker is a
// valid Template that renders to itself.
func Parse(raw json.RawMessage) (*Template, error) {
	t := &Template{raw: raw}
	if !bytes.Contains(raw, []byte(marker)) {
		return t, nil
	}
	t.hasMark = true

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("expr: decode: %w", err)
	}

	env, err := parseOnlyEnv()
	if err != nil {
		return nil, fmt.Errorf("expr: build the cel environment: %w", err)
	}
	if obj, ok := v.(map[string]any); ok {
		t.fields = make(map[string]fieldRefs, len(obj))
		for name, value := range obj {
			a, fieldErr := analyze(env, value)
			if fieldErr != nil {
				return nil, fieldErr
			}
			t.fields[name] = fieldRefs{vars: a.refs.Vars, keys: a.keys}
			t.add(a)
		}
		return t, nil
	}
	a, err := analyze(env, v)
	if err != nil {
		return nil, err
	}
	t.add(a)
	return t, nil
}

// analysis is what the markers inside one value reference.
type analysis struct {
	refs Refs
	keys []keyRef
}

// add folds one value's references into t.
func (t *Template) add(a analysis) {
	t.refs.Needs = append(t.refs.Needs, a.refs.Needs...)
	t.refs.Setup = append(t.refs.Setup, a.refs.Setup...)
	t.refs.Vars = append(t.refs.Vars, a.refs.Vars...)
	t.keys = append(t.keys, a.keys...)
}

// analyze finds the references that every marker inside v makes.
func analyze(env *cel.Env, v any) (analysis, error) {
	var exprs []string
	if err := collectExprs(v, &exprs); err != nil {
		return analysis{}, err
	}
	var out analysis
	for _, exprStr := range exprs {
		needs, setup, err := selectRoots(env, exprStr)
		if err != nil {
			return analysis{}, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		vars, err := varsRoots(env, exprStr)
		if err != nil {
			return analysis{}, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		keys, err := keyRefs(env, exprStr)
		if err != nil {
			return analysis{}, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		out.refs.Needs = append(out.refs.Needs, needs...)
		out.refs.Setup = append(out.refs.Setup, setup...)
		out.refs.Vars = append(out.refs.Vars, vars...)
		out.keys = append(out.keys, keys...)
	}
	return out, nil
}

// Refs reports every step and variable name the markers reference, to check
// against a step's needs list or a variables: block before anything exists to
// evaluate against. The returned slices are shared with t and must not be
// modified.
func (t *Template) Refs() Refs { return t.refs }

// Sensitive reports whether the markers reference a
// needs.<step>.out.<key>, needs.<step>.system.<key>, or
// setup.<step>.out.<key> value that scopes marks Sensitive, or a
// "vars.<name>" that sensitiveVars marks true - so a display row built from
// one field is redacted the way an Outputs row already is.
func (t *Template) Sensitive(scopes Scopes, sensitiveVars map[string]bool) bool {
	return anySensitive(t.keys, t.refs.Vars, scopes, sensitiveVars)
}

// FieldSensitive is [Template.Sensitive] for the markers in one top-level
// field of a template whose value is an object. It reports false for a field
// the template does not have, and for a template that is not an object.
func (t *Template) FieldSensitive(field string, scopes Scopes, sensitiveVars map[string]bool) bool {
	f := t.fields[field]
	return anySensitive(f.keys, f.vars, scopes, sensitiveVars)
}

// anySensitive reports whether any of keys or vars resolves to a sensitive value.
func anySensitive(keys []keyRef, vars []string, scopes Scopes, sensitiveVars map[string]bool) bool {
	for _, ref := range keys {
		if val, ok := ref.lookup(scopes); ok && val.Sensitive {
			return true
		}
	}
	for _, name := range vars {
		if sensitiveVars[name] {
			return true
		}
	}
	return false
}

// Render replaces all the "${cel-expression}" markers found inside strings with the result it computes.
// The value's strings are evaluated against five variables:
//  1. `needs`: keyed by the upstream step's name, each with two sub-namespaces:
//     `out` (the step's own outputs: `needs.<step>.out.<key>`) and `system`
//     (kevin-computed values for that step: `needs.<step>.system.<key>`).
//  2. `setup`: same shape as needs, but keyed by the name of a setup-scope
//     step named via a "setup.<name>" needs entry, a variable separate
//     from needs. Always empty when rendering a setup-scope step itself.
//  3. `env`: [Scopes.Env], e.g. `env.HOME`.
//     A reference to an unset variable errors; use `has(env.FOO) ? env.FOO : "default"` for a fallback.
//  4. `project`: project-level constants kevin computes once per session,
//     such as `project.dir` or `project.root_cert`, keyed by name, same map
//     shape as `env`.
//  5. `vars`: an environment's declared variables: block, keyed by name,
//     same map shape as `env`/`project`.
func (t *Template) Render(step string, scopes Scopes) (json.RawMessage, error) {
	// inexpensive early exit.
	// If we have no '${' cel marker, then there is nothing to evaluate.
	if !t.hasMark {
		return t.raw, nil
	}

	var v any
	if err := json.Unmarshal(t.raw, &v); err != nil {
		return nil, fmt.Errorf("expr: decode: %w", err)
	}

	needs := activation(scopes.Needs, scopes.System)
	setup := activation(scopes.Setup, nil)
	rendered, err := renderValue(v, step, needs, setup, scopes.Env, scopes.Project, scopes.Vars)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(rendered)
	if err != nil {
		return nil, fmt.Errorf("expr: encode: %w", err)
	}
	return out, nil
}
