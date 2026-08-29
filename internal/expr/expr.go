// Package expr renders ${cel-expression} markers found inside a JSON value's strings.
package expr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"

	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/output"
)

// marker opens an expression. Render skips the whole walk when a value carries none.
const marker = "${"

var celEnv = sync.OnceValues(buildEnv)

func buildEnv() (*cel.Env, error) {
	needsType := cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.StringType)))
	env, err := cel.NewEnv(
		cel.Variable("needs", needsType),
		cel.Variable("setup", needsType),
		cel.Variable("env", cel.MapType(cel.StringType, cel.StringType)),
	)
	if err != nil {
		return nil, fmt.Errorf("expr: build the CEL environment: %w", err)
	}
	return env, nil
}

// Render walks a JSON value and replaces all the "${cel-expression}" markers found inside strings with the result it computes.
// The value's strings are evaluated against three variables:
//  1. `needs`: keyed by the upstream step's name, each with two sub-namespaces:
//     `out` (the step's own outputs: `needs.<step>.out.<key>`) and `system`
//     (kevin-computed values for that step: `needs.<step>.system.<key>`).
//  2. `setup`: same shape as needs, but keyed by the name of a setup-scope
//     step named via a "setup.<name>" needs entry, a variable separate
//     from needs. Always empty when rendering a setup-scope step itself.
//  3. `env`: the kevin process's own environment variables, e.g. `env.HOME`.
//     A reference to an unset variable errors; use `has(env.FOO) ? env.FOO : "default"` for a fallback.
func Render(raw json.RawMessage, step string, deps, system, setupDeps map[string]dag.Outputs) (json.RawMessage, error) {
	// inexpensive early exit.
	// If we have no '${' cel marker, then there is nothing to evaluate.
	if !bytes.Contains(raw, []byte(marker)) {
		return raw, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("expr: decode: %w", err)
	}

	needs := activation(deps, system)
	setup := activation(setupDeps, nil)
	env := hostEnv()
	rendered, err := renderValue(v, step, needs, setup, env)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(rendered)
	if err != nil {
		return nil, fmt.Errorf("expr: encode: %w", err)
	}
	return out, nil
}

// renderValue is a recursive function that evaluates CEL expressions in a JSON value.
// If the value `v` is a string, it tries to render any cel expression in the string.
// If the value is a collection type, it will recursively try to evaluate each collection element.
// Otherwise, it returns the value unchanged.
func renderValue(v any, step string, needs, setup map[string]any, env map[string]string) (any, error) {
	switch t := v.(type) {
	case string:
		return renderString(t, step, needs, setup, env)
	case map[string]any:
		for k, elem := range t {
			rendered, err := renderValue(elem, step, needs, setup, env)
			if err != nil {
				return nil, err
			}
			t[k] = rendered
		}
		return t, nil
	case []any:
		for i, elem := range t {
			rendered, err := renderValue(elem, step, needs, setup, env)
			if err != nil {
				return nil, err
			}
			t[i] = rendered
		}
		return t, nil
	default:
		return v, nil
	}
}

// renderString splices the result of every "${...}" expression in s back
// into the surrounding literal text. s with no marker returns unchanged.
func renderString(s string, step string, needs, setup map[string]any, env map[string]string) (string, error) {
	if !strings.Contains(s, marker) {
		return s, nil
	}

	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(rest[:i])

		afterOpen := rest[i+len(marker):]
		exprStr, remainder, ok := strings.Cut(afterOpen, "}")
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrUnbalanced, s)
		}

		result, err := eval(exprStr, step, needs, setup, env)
		if err != nil {
			return "", err
		}
		b.WriteString(result)
		rest = remainder
	}
}

// eval compiles and evaluates one CEL expression against needs, setup and
// env, and requires the result is a string.
func eval(exprStr, step string, needs, setup map[string]any, env map[string]string) (string, error) {
	cEnv, err := celEnv()
	if err != nil {
		return "", fmt.Errorf("expr: build the cel environment: %w", err)
	}

	ast, iss := cEnv.Compile(exprStr)
	if iss != nil && iss.Err() != nil {
		return "", fmt.Errorf("expr: %q: %w", exprStr, iss.Err())
	}
	prg, err := cEnv.Program(ast)
	if err != nil {
		return "", fmt.Errorf("expr: %q: %w", exprStr, err)
	}

	out, _, err := prg.Eval(map[string]any{"needs": needs, "setup": setup, "env": env})
	if err != nil {
		return "", fmt.Errorf("expr: %q: %w (is the step it names listed in %q's needs, or the variable set in the environment?)", exprStr, err, step)
	}

	result, ok := out.Value().(string)
	if !ok {
		return "", fmt.Errorf("expr: %q: must evaluate to a string, got %T", exprStr, out.Value())
	}
	return result, nil
}

// activation converts deps and system into the map[string]any shape CEL's
// default type adapter accepts for the "needs" variable.
//
// A sensitive dep value's content still reaches the rendered "with" block
// here - nothing today logs or displays a rendered "with" block - but a
// plugin that echoes that content back out as its own Outputs or a Detail
// must re-mark it sensitive itself; kevin can't trace a value's
// sensitivity across a process boundary.
func activation(deps, system map[string]dag.Outputs) map[string]any {
	needs := make(map[string]any, len(deps))
	for name, out := range deps {
		needs[name] = map[string]any{
			"out":    stringValues(out),
			"system": stringValues(system[name]),
		}
	}
	return needs
}

// stringValues extracts the string content of o, discarding its
// sensitivity tags, for CEL's "needs.<step>.out.<key>" variable.
func stringValues(o dag.Outputs) map[string]any {
	m := make(map[string]any, len(o))
	for k, v := range o {
		val, _ := v.(output.Value)
		m[k] = val.String
	}
	return m
}

// hostEnv converts os.Environ() into the map[string]string shape CEL's
// default type adapter accepts for the "env" variable.
func hostEnv() map[string]string {
	environ := os.Environ()
	env := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	return env
}
