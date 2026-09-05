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

	// ErrUndeclaredNeed reports a with block's "${...}" expression naming a
	// step that the step's own needs list does not declare.
	ErrUndeclaredNeed = Error("config: with block references a step its needs list does not declare")

	// ErrReservedKeyChar reports a step, group, or group member key
	// containing '.' - reserved for a group member's own internal
	// "<group>.<member>" name.
	ErrReservedKeyChar = Error("config: key may not contain '.', that syntax is reserved for a group member's internal name")

	// ErrUnaddressableMember reports a needs entry that names a group
	// member's internal "<group>.<member>" name directly - a group's
	// members are addressable only through the group's own name.
	ErrUnaddressableMember = Error("config: needs references a group member by its internal name, a group is addressable only by its own name")

	// ErrUnknownStep reports a command's needs list naming a step that does
	// not exist in the scope the entry implies.
	ErrUnknownStep = Error("config: needs references a step that does not exist")

	// ErrExportNotSupported reports a command's needs list naming a step
	// whose plugin does not implement Export for that step type.
	ErrExportNotSupported = Error("config: needs references a step that does not implement export")

	// ErrPackageConflict reports a required environment file that declares
	// no CUE package while another .cue file in the same directory does.
	ErrPackageConflict = Error("config: a CUE file in the project directory declares a package, but the environment file does not")

	// ErrTagWithoutPackage reports a non-empty tags argument to [Load]
	// against an environment file that declares no CUE package.
	ErrTagWithoutPackage = Error("config: --tag/-t requires the environment file to declare a CUE package")
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
