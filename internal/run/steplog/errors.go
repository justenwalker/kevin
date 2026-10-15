package steplog

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNotFound reports that no durable log file exists yet for this
	// environment - kevin run or kevin setup has never started it.
	ErrNotFound = Error("steplog: no log file for this environment")

	// ErrInvalidCursor reports a cursor that isn't one ReadSince returned -
	// malformed, or from an incompatible steplog version.
	ErrInvalidCursor = Error("steplog: unrecognized cursor")

	// ErrStaleCursor reports a cursor pointing past the current log
	// file's end - almost always one from an earlier run, since
	// logs.ndjson is truncated fresh on every kevin run/setup start.
	ErrStaleCursor = Error("steplog: cursor is from a different or earlier run")
)
