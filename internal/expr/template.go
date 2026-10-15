package expr

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Template is a JSON value whose "${...}" markers are parsed. Parse a with or
// config block once; its references and sensitivity are then read without
// parsing it again.
type Template struct {
	raw     json.RawMessage
	refs    Refs
	keys    []keyRef
	hasMark bool
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
	var exprs []string
	if err := collectExprs(v, &exprs); err != nil {
		return nil, err
	}

	env, err := parseOnlyEnv()
	if err != nil {
		return nil, fmt.Errorf("expr: build the cel environment: %w", err)
	}
	for _, exprStr := range exprs {
		needs, setup, err := selectRoots(env, exprStr)
		if err != nil {
			return nil, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		vars, err := varsRoots(env, exprStr)
		if err != nil {
			return nil, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		keys, err := keyRefs(env, exprStr)
		if err != nil {
			return nil, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		t.refs.Needs = append(t.refs.Needs, needs...)
		t.refs.Setup = append(t.refs.Setup, setup...)
		t.refs.Vars = append(t.refs.Vars, vars...)
		t.keys = append(t.keys, keys...)
	}
	return t, nil
}

// Refs reports every step and variable name the markers reference, to check
// against a step's needs list or a variables: block before anything exists to
// evaluate against.
func (t *Template) Refs() Refs { return t.refs }

// Sensitive reports whether the markers reference a
// needs.<step>.out.<key>, needs.<step>.system.<key>, or
// setup.<step>.out.<key> value that scopes marks Sensitive, or a
// "vars.<name>" that sensitiveVars marks true - so a display row built from
// one field is redacted the way an Outputs row already is.
func (t *Template) Sensitive(scopes Scopes, sensitiveVars map[string]bool) bool {
	for _, ref := range t.keys {
		if val, ok := ref.lookup(scopes); ok && val.Sensitive {
			return true
		}
	}
	for _, name := range t.refs.Vars {
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
