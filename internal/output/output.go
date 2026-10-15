// Package output defines the value type a step publishes to its dependents.
package output

// Value is one output value a step produces.
type Value struct {
	// String is the value's content.
	String string

	// Sensitive is true when String must never be logged or displayed in
	// full - a generated password or token, for example.
	Sensitive bool
}

// Map is a step's outputs, keyed by output name. Its dependents receive it
// when the step is walked.
type Map map[string]Value
