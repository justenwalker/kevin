package expr

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnbalanced reports a "${" with no matching "}".
const ErrUnbalanced = Error(`expr: unbalanced "${"`)
