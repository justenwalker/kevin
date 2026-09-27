package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/varfile"
)

// Variable is one declared entry of an environment's variables: block.
type Variable struct {
	// Default is this variable's value when nothing external supplies
	// one. Required (an error from [Config.ResolveVariables]) when nil.
	Default *string `json:"default"`

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
// supplies, and [ErrMalformedVariable] for a Set entry with no "=".
func (cfg *Config) ResolveVariables(vars VariableInputs) error {
	fileValues, err := fileVariableValues(vars.File)
	if err != nil {
		return err
	}
	setValues, err := parseVarArgs(vars.Set)
	if err != nil {
		return err
	}

	values := make(map[string]string, len(cfg.Variables))
	for name, decl := range cfg.Variables {
		value, ok := setValues[name]
		if !ok {
			value, ok = os.LookupEnv(variableEnvName(name))
		}
		if !ok {
			value, ok = fileValues[name]
		}
		if !ok && decl.Default != nil {
			value, ok = *decl.Default, true
		}
		if !ok {
			return fmt.Errorf("config: variables.%s: %w", name, ErrRequiredVariable)
		}
		values[name] = value
	}
	cfg.VariableValues = values
	return nil
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
