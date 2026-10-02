package relaytest

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNoRepoRoot reports that Build cannot find the repository root to build
// kevin-relay from.
const ErrNoRepoRoot = Error("relaytest: cannot locate the repository root")
