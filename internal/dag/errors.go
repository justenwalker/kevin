package dag

import (
	"errors"
	"fmt"
)

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrCycle reports that the graph has no valid order.
	ErrCycle = Error("dag: graph contains a cycle")

	// ErrUnknownStep reports a dependency on a step that does not exist.
	ErrUnknownStep = Error("dag: unknown step")
)

// CycleError reports that the graph has a cycle.
type CycleError struct {
	Steps []string
}

// Error returns the error text for a CycleError.
func (e *CycleError) Error() string {
	return fmt.Sprintf("%v: %v", ErrCycle, e.Steps)
}

// Is reports whether err is ErrCycle, so errors.Is(err, ErrCycle) matches a CycleError.
func (e *CycleError) Is(err error) bool {
	return errors.Is(err, ErrCycle)
}

// Unwrap returns ErrCycle.
func (e *CycleError) Unwrap() error {
	return ErrCycle
}
