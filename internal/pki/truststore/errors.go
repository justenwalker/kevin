package truststore

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNeedsRoot reports that a store needs root and the process is not
// root. The Result carries the command to run.
const ErrNeedsRoot = Error("truststore: the store needs root")
