package config

import "fmt"

// Scope is the DAG a step belongs to.
type Scope uint8

// Scopes are the two independent DAGs in an environment.
const (
	// ScopeEnv is the ephemeral DAG that kevin run brings up and tears down.
	ScopeEnv Scope = iota + 1

	// ScopeSetup is the DAG that persists across runs.
	ScopeSetup
)

// Valid reports whether s is one of the two scopes.
func (s Scope) Valid() bool {
	return s == ScopeEnv || s == ScopeSetup
}

// String returns "env" or "setup", the form used in kevin.cue, container
// labels, and messages.
func (s Scope) String() string {
	switch s {
	case ScopeEnv:
		return "env"
	case ScopeSetup:
		return "setup"
	default:
		return fmt.Sprintf("Scope(%d)", uint8(s))
	}
}
