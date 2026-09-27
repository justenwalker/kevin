package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"

	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/varfile"
)

// Variable is one declared entry of an environment's variables: block.
type Variable struct {
	// Type is the raw CUE source of the variable's "type" constraint (a
	// bare kind, a range, a disjunction, a struct shape, ...), or nil when
	// the variables: block omits it - a plain string, same as Type being
	// literally "string". It never comes through JSON: a constraint is
	// often non-concrete (e.g. "int & >=1 & <=10"), which JSON can't
	// represent - [File.Validate] and [File.Config] populate it directly
	// from the parsed CUE value instead.
	Type []byte `json:"-"`

	// Default is this variable's value when nothing external supplies
	// one, as CUE/JSON source checked against Type. Required (an error
	// from [Config.ResolveVariables]) when nil.
	Default json.RawMessage `json:"default"`

	// Sensitive marks this variable's value as secret: a with block field
	// that reads it via "${vars.<name>}" is always redacted wherever the
	// console or MCP server shows it.
	Sensitive bool `json:"sensitive"`
}

// VariableInputs is where a variables: block's values come from outside
// kevin.cue, highest precedence first: Set (one or more "--var
// KEY=VALUE"), then a KEVIN_VAR_<NAME> environment variable, then File's
// own contents, then the variable's own declared Default.
type VariableInputs struct {
	// File is a var-file path ("KEY=VALUE" per line), or "" for none.
	File string

	// Set holds each "--var KEY=VALUE" argument, unparsed.
	Set []string
}

// ResolveVariables resolves every variable cfg.Variables declares against
// vars and stores the result in cfg.VariableValues. It returns
// [ErrRequiredVariable] for a variable with no default that nothing
// supplies, [ErrMalformedVariable] for a Set entry with no "=", and
// [ErrVariableValue] for a value that does not satisfy its own declared
// type.
func (cfg *Config) ResolveVariables(vars VariableInputs) error {
	fileValues, err := fileVariableValues(vars.File)
	if err != nil {
		return err
	}
	setValues, err := parseVarArgs(vars.Set)
	if err != nil {
		return err
	}

	ctx := cuecontext.New()
	values := make(map[string]any, len(cfg.Variables))
	for name, decl := range cfg.Variables {
		typeVal, err := compileVariableType(ctx, decl.Type)
		if err != nil {
			return fmt.Errorf("config: variables.%s.type: %w", name, err)
		}

		var candidate cue.Value
		envValue, envSet := os.LookupEnv(variableEnvName(name))
		fileValue, fileSet := fileValues[name]
		setValue, setSet := setValues[name]
		switch {
		case setSet:
			candidate, err = decodeVariableString(ctx, typeVal, setValue)
		case envSet:
			candidate, err = decodeVariableString(ctx, typeVal, envValue)
		case fileSet:
			candidate, err = decodeVariableString(ctx, typeVal, fileValue)
		case decl.Default != nil:
			candidate = ctx.CompileBytes(decl.Default)
			err = candidate.Err()
		default:
			return fmt.Errorf("config: variables.%s: %w", name, ErrRequiredVariable)
		}
		if err != nil {
			return fmt.Errorf("config: variables.%s: %w", name, err)
		}

		value, err := unifyVariableValue(typeVal, candidate)
		if err != nil {
			return fmt.Errorf("config: variables.%s: %w: %w", name, ErrVariableValue, err)
		}
		values[name] = value
	}
	cfg.VariableValues = values
	return nil
}

// compileVariableType compiles decl's raw "type" source (see
// [Variable.Type]) into the constraint a candidate value must satisfy - an
// implicit "string" when decl declares no type, the unchanged behavior for
// every variable that never sets one.
func compileVariableType(ctx *cue.Context, typeSrc []byte) (cue.Value, error) {
	if len(typeSrc) == 0 {
		typeSrc = []byte("string")
	}
	v := ctx.CompileBytes(typeSrc)
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("%w", err)
	}
	return v, nil
}

// decodeVariableString turns raw - a value from a var-file, KEVIN_VAR_<NAME>,
// or --var, always a shell string - into a [cue.Value] to unify against
// typeVal: taken literally when typeVal accepts only a string (the common
// case, and every variable that declares no type), compiled as CUE syntax
// otherwise, so "--var replicas=3" becomes the int 3, "--var strict=true"
// becomes the bool true, and "--var tags='[\"a\",\"b\"]'" becomes a list.
func decodeVariableString(ctx *cue.Context, typeVal cue.Value, raw string) (cue.Value, error) {
	if typeVal.IncompleteKind() == cue.StringKind {
		return ctx.Encode(raw), nil
	}
	v := ctx.CompileString(raw)
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("%w", err)
	}
	return v, nil
}

// unifyVariableValue unifies candidate against typeVal, requires the result
// is concrete, and decodes it to a native Go value.
func unifyVariableValue(typeVal, candidate cue.Value) (any, error) {
	merged := typeVal.Unify(candidate)
	if err := merged.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return decodeNative(merged)
}

// decodeNative decodes v - a concrete [cue.Value] - to its native Go value,
// via CUE's own per-kind accessors for a scalar (so an int stays an int64,
// never encoding/json's float64) and [cue.Value.Decode] for a list or
// struct.
func decodeNative(v cue.Value) (any, error) {
	switch v.Kind() {
	case cue.IntKind:
		i, err := v.Int64()
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return i, nil
	case cue.FloatKind, cue.NumberKind:
		f, err := v.Float64()
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return f, nil
	case cue.BoolKind:
		b, err := v.Bool()
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return b, nil
	case cue.StringKind:
		s, err := v.String()
		if err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return s, nil
	case cue.NullKind:
		return nil, nil //nolint:nilnil // null is a legitimate variable value
	default:
		var out any
		if err := v.Decode(&out); err != nil {
			return nil, fmt.Errorf("%w", err)
		}
		return out, nil
	}
}

// fileVariableValues parses path, or reports no values for an empty path.
func fileVariableValues(path string) (map[string]string, error) {
	if path == "" {
		return nil, nil //nolint:nilnil // no file means no values, a valid empty result
	}
	values, err := varfile.ParseFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return values, nil
}

// parseVarArgs splits each "KEY=VALUE" entry of args.
func parseVarArgs(args []string) (map[string]string, error) {
	if len(args) == 0 {
		return nil, nil //nolint:nilnil // no args means no values, a valid empty result
	}
	values := make(map[string]string, len(args))
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrMalformedVariable, arg)
		}
		values[key] = value
	}
	return values, nil
}

// variableEnvName is the KEVIN_VAR_<NAME> environment variable that
// supplies name's value, name upper-cased.
func variableEnvName(name string) string {
	return "KEVIN_VAR_" + strings.ToUpper(name)
}

// SensitiveVariables reports the names of cfg's declared variables whose
// sensitive: true.
func (cfg *Config) SensitiveVariables() map[string]bool {
	sensitive := make(map[string]bool, len(cfg.Variables))
	for name, decl := range cfg.Variables {
		if decl.Sensitive {
			sensitive[name] = true
		}
	}
	return sensitive
}

// validateVarReferences checks that every "vars.<name>" reference inside
// raw's "${...}" markers names a variable that declared actually declares -
// the same reasoning validateNeedsReferences applies to needs/setup. field
// names the block in an error message ("with" for a step, "run" for a
// command).
func validateVarReferences(scopeName, step, field string, declared map[string]Variable, raw json.RawMessage) error {
	refs, err := expr.ReferencedVars(raw)
	if err != nil {
		return fmt.Errorf("config: %s.%s.%s: %w", scopeName, step, field, err)
	}
	for _, name := range refs {
		if _, ok := declared[name]; !ok {
			return fmt.Errorf("config: %s.%s.%s: references %q via vars.%s, but variables.%s is not declared: %w",
				scopeName, step, field, name, name, name, ErrUndeclaredVariable)
		}
	}
	return nil
}
