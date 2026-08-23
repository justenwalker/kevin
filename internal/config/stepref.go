package config

import (
	"fmt"
	"sort"
	"strings"
)

// reservedNames lists every plugin name that a plugin cannot declare. The
// list stays sorted, so an error message names every entry in order.
var reservedNames = []string{
	"builtin",
	"cmd",
	"core",
	"docker",
	"file",
	"helm",
	"http",
	"k8s",
	"kevin",
	"kubectl",
	"kubernetes",
	"oci",
	"official",
	"std",
}

// StepRef names a step type, as <plugin>:<step>.
type StepRef struct {
	Plugin string
	Step   string
}

// String renders a reference in the form that [ParseStepRef] reads.
func (r StepRef) String() string {
	return r.Plugin + ":" + r.Step
}

// ParseStepRef parses a step type reference.
// A step type reference is two identifiers separated by a colon.
// Example: "builtin:wait"
//
// Returns [ErrBadStepRef] when s does not parse.
func ParseStepRef(s string) (StepRef, error) {
	plugin, step, found := strings.Cut(s, ":")
	if !found {
		return StepRef{}, fmt.Errorf("%w: %q is not a step type, write <plugin>:<step>, such as builtin:%s",
			ErrBadStepRef, s, s)
	}
	if !isValidIdentifier(plugin) || !isValidIdentifier(step) {
		return StepRef{}, fmt.Errorf("%w: %q", ErrBadStepRef, s)
	}
	return StepRef{Plugin: plugin, Step: step}, nil
}

// isValidIdentifier reports whether s is a valid identifier.
//
// A valid identifier must:
// - Be non-empty
// - Not begin or end with '-'
// - Contain only lowercase letters, digits, and hyphens.
func isValidIdentifier(s string) bool {
	if s == "" || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// IsReservedName reports whether a plugin name is reserved.
func IsReservedName(name string) bool {
	i := sort.SearchStrings(reservedNames, name)
	return i < len(reservedNames) && reservedNames[i] == name
}
