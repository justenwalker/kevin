package config

import (
	"strings"

	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/token"
)

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNotFound reports that the project directory has no matching
	// environment file.
	ErrNotFound = Error("config: no kevin environment file found in project directory")

	// ErrAmbiguous reports that the project directory has more than one
	// file matching the requested environment.
	ErrAmbiguous = Error("config: multiple kevin environment files found in project directory")

	// ErrUnknownPlugin reports a step that names an undeclared plugin.
	ErrUnknownPlugin = Error("config: step names an undeclared plugin")

	// ErrInvalid reports that the environment definition does not match the
	// schema.
	ErrInvalid = Error("config: invalid environment")

	// ErrReservedNamespace reports a plugins key that names a plugin that
	// kevin reserves for itself.
	ErrReservedNamespace = Error("config: plugins key uses a reserved namespace")

	// ErrUnknownStepType reports a step that names a step type its plugin
	// does not offer.
	ErrUnknownStepType = Error("config: step names a step type the plugin does not offer")

	// ErrConfigNotSupported reports a plugins config block for a plugin that
	// publishes no config schema.
	ErrConfigNotSupported = Error("config: plugin takes no configuration")

	// ErrBadStepRef reports a step type reference that does not match the
	// grammar of [ParseStepRef].
	ErrBadStepRef = Error("config: invalid step type reference")
)

// ValidationError indicates a problem validating a kevin configuration file.
type ValidationError struct {
	// Pos is the position of the problem in the source.
	Pos []token.Pos

	// Err is the wrapped error.
	Err error

	cfg *cueerrors.Config
}

func (e *ValidationError) Error() string {
	return strings.TrimRight(cueerrors.Details(e.Err, e.cfg), "\n")
}

func (e *ValidationError) Unwrap() error { return e.Err }

// newValidationError builds a *ValidationError describing err, rooted at
// dir (so a printed position renders relative to the project directory).
func newValidationError(dir string, err error) *ValidationError {
	return &ValidationError{Err: err, Pos: cueerrors.Positions(err), cfg: &cueerrors.Config{Cwd: dir}}
}
