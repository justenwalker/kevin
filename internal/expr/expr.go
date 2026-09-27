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
	celast "github.com/google/cel-go/common/ast"

	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/output"
)

// marker opens an expression. Render skips the whole walk when a value carries none.
const marker = "${"

// The CEL variable names a marker's select chain roots at ("needs"/"setup"),
// and the two namespaces beneath each dependency's name ("out"/"system").
const (
	rootNeeds = "needs"
	rootSetup = "setup"
	rootVars  = "vars"
	nsOut     = "out"
	nsSystem  = "system"
)

var celEnv = sync.OnceValues(buildEnv)

func buildEnv() (*cel.Env, error) {
	needsType := cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.StringType)))
	env, err := cel.NewEnv(
		cel.Variable(rootNeeds, needsType),
		cel.Variable(rootSetup, needsType),
		cel.Variable("env", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("project", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable(rootVars, cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("expr: build the CEL environment: %w", err)
	}
	return env, nil
}

// Scopes bundles the caller-supplied values Render evaluates a with block's
// "${...}" markers against. The zero value renders only `env` expressions.
type Scopes struct {
	// Needs supplies `needs.<step>.out.<key>`: each upstream step's own Result.Outputs.
	Needs map[string]dag.Outputs

	// System supplies `needs.<step>.system.<key>`: kevin-computed values for that step.
	System map[string]dag.Outputs

	// Setup supplies `setup.<name>.out.<key>`, for a cross-scope "setup.<name>" needs entry.
	Setup map[string]dag.Outputs

	// Project supplies `project.<key>`: project-level constants kevin computes once per session.
	Project map[string]string

	// Vars supplies `vars.<name>`: an environment's declared variables:
	// block, resolved from a var-file, a KEVIN_VAR_<NAME> environment
	// variable, a "--var" argument, or the variable's own declared
	// default. A value can be any JSON-representable type (string, int64,
	// float64, bool, []any, map[string]any, nil), per the variable's own
	// declared type - unlike needs/setup/env/project, which are always
	// strings.
	Vars map[string]any
}

// Render walks a JSON value and replaces all the "${cel-expression}" markers found inside strings with the result it computes.
// The value's strings are evaluated against five variables:
//  1. `needs`: keyed by the upstream step's name, each with two sub-namespaces:
//     `out` (the step's own outputs: `needs.<step>.out.<key>`) and `system`
//     (kevin-computed values for that step: `needs.<step>.system.<key>`).
//  2. `setup`: same shape as needs, but keyed by the name of a setup-scope
//     step named via a "setup.<name>" needs entry, a variable separate
//     from needs. Always empty when rendering a setup-scope step itself.
//  3. `env`: the kevin process's own environment variables, e.g. `env.HOME`.
//     A reference to an unset variable errors; use `has(env.FOO) ? env.FOO : "default"` for a fallback.
//  4. `project`: project-level constants kevin computes once per session,
//     such as `project.dir` or `project.root_cert`, keyed by name, same map
//     shape as `env`.
//  5. `vars`: an environment's declared variables: block, keyed by name,
//     same map shape as `env`/`project`.
func Render(raw json.RawMessage, step string, scopes Scopes) (json.RawMessage, error) {
	// inexpensive early exit.
	// If we have no '${' cel marker, then there is nothing to evaluate.
	if !bytes.Contains(raw, []byte(marker)) {
		return raw, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("expr: decode: %w", err)
	}

	needs := activation(scopes.Needs, scopes.System)
	setup := activation(scopes.Setup, nil)
	env := hostEnv()
	rendered, err := renderValue(v, step, needs, setup, env, scopes.Project, scopes.Vars)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(rendered)
	if err != nil {
		return nil, fmt.Errorf("expr: encode: %w", err)
	}
	return out, nil
}

// parseOnlyEnv is a CEL environment with no declared variables, sufficient
// to parse an expression's syntax tree without evaluating it - ReferencedSteps
// only walks the tree structure, so it needs no type information.
var parseOnlyEnv = sync.OnceValues(func() (*cel.Env, error) { return cel.NewEnv() })

// ReferencedSteps reports every step name that raw's "${...}" markers
// reference via "needs.<step>..." or "setup.<step>...", with no
// evaluation - for a caller that wants to check those names against a
// step's own needs list statically, before any of Render's variables
// (upstream outputs, in particular) exist to evaluate against.
func ReferencedSteps(raw json.RawMessage) ([]string, []string, error) {
	if !bytes.Contains(raw, []byte(marker)) {
		return nil, nil, nil
	}

	var v any
	if unmarshalErr := json.Unmarshal(raw, &v); unmarshalErr != nil {
		return nil, nil, fmt.Errorf("expr: decode: %w", unmarshalErr)
	}

	var exprs []string
	if collectErr := collectExprs(v, &exprs); collectErr != nil {
		return nil, nil, collectErr
	}

	env, err := parseOnlyEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("expr: build the cel environment: %w", err)
	}
	var needsRefs, setupRefs []string
	for _, exprStr := range exprs {
		n, s, err := selectRoots(env, exprStr)
		if err != nil {
			return nil, nil, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		needsRefs = append(needsRefs, n...)
		setupRefs = append(setupRefs, s...)
	}
	return needsRefs, setupRefs, nil
}

// ReferencedVars reports every variable name that raw's "${...}" markers
// reference via "vars.<name>", with no evaluation - for a caller that wants
// to check those names against a declared variables: block statically,
// before Render's "vars" variable exists to evaluate against.
func ReferencedVars(raw json.RawMessage) ([]string, error) {
	if !bytes.Contains(raw, []byte(marker)) {
		return nil, nil
	}

	var v any
	if unmarshalErr := json.Unmarshal(raw, &v); unmarshalErr != nil {
		return nil, fmt.Errorf("expr: decode: %w", unmarshalErr)
	}

	var exprs []string
	if collectErr := collectExprs(v, &exprs); collectErr != nil {
		return nil, collectErr
	}

	env, err := parseOnlyEnv()
	if err != nil {
		return nil, fmt.Errorf("expr: build the cel environment: %w", err)
	}
	var varRefs []string
	for _, exprStr := range exprs {
		refs, err := varsRoots(env, exprStr)
		if err != nil {
			return nil, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		varRefs = append(varRefs, refs...)
	}
	return varRefs, nil
}

// varsRoots reports the field name of every "vars.<field>" select chain in
// exprStr - a chain rooted at an identifier named "vars" - the same
// single-level shape selectRoots recognizes for "needs"/"setup", but "vars"
// itself is a leaf value, not a further-nested namespace.
func varsRoots(env *cel.Env, exprStr string) ([]string, error) {
	parsed, iss := env.Parse(exprStr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("expr: parse: %w", iss.Err())
	}

	var refs []string
	root := parsed.NativeRep().Expr()
	celast.PreOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		operand := sel.Operand()
		if operand.Kind() != celast.IdentKind || operand.AsIdent() != rootVars {
			return
		}
		refs = append(refs, sel.FieldName())
	}))
	return refs, nil
}

// collectExprs walks v the same way renderValue does, appending every
// "${...}" expression found in a string value to exprs, with no
// evaluation.
func collectExprs(v any, exprs *[]string) error {
	switch t := v.(type) {
	case string:
		found, err := markersIn(t)
		if err != nil {
			return err
		}
		*exprs = append(*exprs, found...)
	case map[string]any:
		for _, elem := range t {
			if err := collectExprs(elem, exprs); err != nil {
				return err
			}
		}
	case []any:
		for _, elem := range t {
			if err := collectExprs(elem, exprs); err != nil {
				return err
			}
		}
	}
	return nil
}

// selectRoots parses exprStr and reports the field name of every
// "needs.<field>..." and "setup.<field>..." select chain in it - a chain
// rooted at an identifier named "needs" or "setup", whatever comes after.
func selectRoots(env *cel.Env, exprStr string) ([]string, []string, error) {
	parsed, iss := env.Parse(exprStr)
	if iss != nil && iss.Err() != nil {
		return nil, nil, fmt.Errorf("expr: parse: %w", iss.Err())
	}

	var needsRefs, setupRefs []string
	root := parsed.NativeRep().Expr()
	celast.PreOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		sel := e.AsSelect()
		operand := sel.Operand()
		if operand.Kind() != celast.IdentKind {
			return
		}
		switch operand.AsIdent() {
		case rootNeeds:
			needsRefs = append(needsRefs, sel.FieldName())
		case rootSetup:
			setupRefs = append(setupRefs, sel.FieldName())
		}
	}))
	return needsRefs, setupRefs, nil
}

// FieldSensitive reports whether raw's "${...}" markers reference a
// needs.<step>.out.<key>, needs.<step>.system.<key>, or
// setup.<step>.out.<key> value that scopes marks Sensitive - for a caller
// building a display row from one `with` field who wants to redact it the
// same way an Outputs row already is, without inventing a second
// sensitivity convention. raw with no marker, or none referencing a
// sensitive value, reports false.
// FieldSensitive additionally reports true when raw references, via
// "vars.<name>", a name sensitiveVars marks true - the declared
// counterpart to scopes' own Sensitive-tagged values, for a variable whose
// sensitivity is a fact about its declared name rather than something
// carried by a particular value.
func FieldSensitive(raw json.RawMessage, scopes Scopes, sensitiveVars map[string]bool) (bool, error) {
	if !bytes.Contains(raw, []byte(marker)) {
		return false, nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false, fmt.Errorf("expr: decode: %w", err)
	}
	var exprs []string
	if err := collectExprs(v, &exprs); err != nil {
		return false, err
	}

	env, err := parseOnlyEnv()
	if err != nil {
		return false, fmt.Errorf("expr: build the cel environment: %w", err)
	}
	for _, exprStr := range exprs {
		refs, err := keyRefs(env, exprStr)
		if err != nil {
			return false, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		for _, ref := range refs {
			if val, ok := ref.lookup(scopes); ok && val.Sensitive {
				return true, nil
			}
		}

		varRefs, err := varsRoots(env, exprStr)
		if err != nil {
			return false, fmt.Errorf("expr: %q: %w", exprStr, err)
		}
		for _, name := range varRefs {
			if sensitiveVars[name] {
				return true, nil
			}
		}
	}
	return false, nil
}

// keyRef names one needs/setup value a "${...}" expression's select chain
// reaches: root is "needs" or "setup", step is the dependency's name, ns is
// "out" or "system", and key is the value's own name within that namespace.
type keyRef struct {
	root, step, ns, key string
}

// lookup resolves ref against scopes, reporting the value and whether it
// was found - false for a namespace/root combination scopes doesn't carry
// (e.g. "setup.<name>.system", which doesn't exist) or a step/key scopes
// has no entry for.
func (ref keyRef) lookup(scopes Scopes) (output.Value, bool) {
	var deps map[string]dag.Outputs
	switch {
	case ref.root == rootNeeds && ref.ns == nsOut:
		deps = scopes.Needs
	case ref.root == rootNeeds && ref.ns == nsSystem:
		deps = scopes.System
	case ref.root == rootSetup && ref.ns == nsOut:
		deps = scopes.Setup
	default:
		return output.Value{}, false
	}
	val, ok := deps[ref.step][ref.key].(output.Value)
	return val, ok
}

// keyRefs parses exprStr and reports every "needs.<step>.<ns>.<key>" or
// "setup.<step>.<ns>.<key>" select chain in it, rooted at an identifier
// named "needs" or "setup" - the same three-level shape Render's "needs"/
// "setup" CEL variables expose, one level deeper than selectRoots walks.
func keyRefs(env *cel.Env, exprStr string) ([]keyRef, error) {
	parsed, iss := env.Parse(exprStr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("expr: parse: %w", iss.Err())
	}

	var refs []keyRef
	root := parsed.NativeRep().Expr()
	celast.PreOrderVisit(root, celast.NewExprVisitor(func(e celast.Expr) {
		if e.Kind() != celast.SelectKind {
			return
		}
		keySel := e.AsSelect()
		if keySel.Operand().Kind() != celast.SelectKind {
			return
		}
		nsSel := keySel.Operand().AsSelect()
		if nsSel.Operand().Kind() != celast.SelectKind {
			return
		}
		stepSel := nsSel.Operand().AsSelect()
		if stepSel.Operand().Kind() != celast.IdentKind {
			return
		}
		rootName := stepSel.Operand().AsIdent()
		if rootName != rootNeeds && rootName != rootSetup {
			return
		}
		refs = append(refs, keyRef{root: rootName, step: stepSel.FieldName(), ns: nsSel.FieldName(), key: keySel.FieldName()})
	}))
	return refs, nil
}

// renderValue is a recursive function that evaluates CEL expressions in a JSON value.
// If the value `v` is a string whose entire content is one bare "${...}"
// marker, it evaluates to that expression's native result (a string, number,
// bool, list, map, or nil), replacing the string leaf outright. If the value
// is a string carrying a marker alongside other text, it splices the
// expression's string result into that text - which requires the result be
// a string. If the value is a collection type, it recursively evaluates
// each element. Otherwise, it returns the value unchanged.
func renderValue(v any, step string, needs, setup map[string]any, env, project map[string]string, variables map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		if exprStr, ok := BareMarker(t); ok {
			return evalTyped(exprStr, step, needs, setup, env, project, variables)
		}
		return renderString(t, step, needs, setup, env, project, variables)
	case map[string]any:
		for k, elem := range t {
			rendered, err := renderValue(elem, step, needs, setup, env, project, variables)
			if err != nil {
				return nil, err
			}
			t[k] = rendered
		}
		return t, nil
	case []any:
		for i, elem := range t {
			rendered, err := renderValue(elem, step, needs, setup, env, project, variables)
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

// BareMarker reports the CEL expression inside s, and whether s's entire
// content is exactly one "${...}" marker with no surrounding literal text -
// the leaf shape renderValue substitutes a native, non-string value into
// rather than splicing a string. internal/config reuses this to identify a
// "with"/"config" field it must defer a schema type check on, using the
// same parsing renderString's splicing already does.
func BareMarker(s string) (string, bool) {
	before, exprStr, remainder, found, err := nextMarker(s)
	if err != nil || !found || before != "" || remainder != "" {
		return "", false
	}
	return exprStr, true
}

// renderString splices the result of every "${...}" expression in s back
// into the surrounding literal text. s with no marker returns unchanged.
func renderString(s string, step string, needs, setup map[string]any, env, project map[string]string, variables map[string]any) (string, error) {
	if !strings.Contains(s, marker) {
		return s, nil
	}

	var b strings.Builder
	rest := s
	for {
		before, exprStr, remainder, ok, err := nextMarker(rest)
		if err != nil {
			return "", err
		}
		if !ok {
			b.WriteString(rest)
			return b.String(), nil
		}
		b.WriteString(before)

		result, err := eval(exprStr, step, needs, setup, env, project, variables)
		if err != nil {
			return "", err
		}
		b.WriteString(result)
		rest = remainder
	}
}

// nextMarker finds the first "${...}" marker in s. before is the literal
// text ahead of it, exprStr is the CEL expression inside it, and rest is
// the text after its closing "}". ok is false when s carries no (more)
// marker, the loop-termination signal both renderString and markersIn
// share. An unclosed marker is reported as ErrUnbalanced.
func nextMarker(s string) (string, string, string, bool, error) {
	before, afterOpen, found := strings.Cut(s, marker)
	if !found {
		return "", "", "", false, nil
	}
	exprStr, rest, closed := strings.Cut(afterOpen, "}")
	if !closed {
		return "", "", "", false, fmt.Errorf("%w: %q", ErrUnbalanced, s)
	}
	return before, exprStr, rest, true, nil
}

// markersIn returns every CEL expression found inside s's "${...}" markers,
// in order, with no evaluation - the same splitting nextMarker gives
// renderString, for a caller that only needs to inspect the expressions
// themselves.
func markersIn(s string) ([]string, error) {
	var exprs []string
	rest := s
	for {
		_, exprStr, remainder, ok, err := nextMarker(rest)
		if err != nil {
			return nil, err
		}
		if !ok {
			return exprs, nil
		}
		exprs = append(exprs, exprStr)
		rest = remainder
	}
}

// eval compiles and evaluates one CEL expression against needs, setup, env,
// project, and variables, and requires the result is a string - for a
// marker spliced into surrounding literal text, which only a string result
// can be concatenated into.
func eval(exprStr, step string, needs, setup map[string]any, env, project map[string]string, variables map[string]any) (string, error) {
	val, err := compileAndEval(exprStr, step, needs, setup, env, project, variables)
	if err != nil {
		return "", err
	}
	result, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("expr: %q: must evaluate to a string when interpolated into surrounding text, got %T", exprStr, val)
	}
	return result, nil
}

// evalTyped compiles and evaluates one CEL expression the same way eval
// does, but returns its native result unconverted - for a bare marker
// occupying a whole leaf, which can become any JSON-representable value,
// not only a string.
func evalTyped(exprStr, step string, needs, setup map[string]any, env, project map[string]string, variables map[string]any) (any, error) {
	return compileAndEval(exprStr, step, needs, setup, env, project, variables)
}

// compileAndEval compiles exprStr, evaluates it against needs, setup, env,
// project, and variables, and returns its native Go value (the shared step
// eval and evalTyped build on).
func compileAndEval(exprStr, step string, needs, setup map[string]any, env, project map[string]string, variables map[string]any) (any, error) {
	cEnv, err := celEnv()
	if err != nil {
		return nil, fmt.Errorf("expr: build the cel environment: %w", err)
	}

	ast, iss := cEnv.Compile(exprStr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("expr: %q: %w", exprStr, iss.Err())
	}
	prg, err := cEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("expr: %q: %w", exprStr, err)
	}

	out, _, err := prg.Eval(map[string]any{rootNeeds: needs, rootSetup: setup, "env": env, "project": project, rootVars: variables})
	if err != nil {
		return nil, fmt.Errorf("expr: %q: %w (is the step it names listed in %q's needs, or the variable set in the environment?)", exprStr, err, step)
	}
	return out.Value(), nil
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
			nsOut:    stringValues(out),
			nsSystem: stringValues(system[name]),
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
