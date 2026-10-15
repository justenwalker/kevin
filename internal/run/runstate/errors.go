package runstate

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrAlreadyRunning reports that a live run holds the state: a second run
// against the same project and environment would race the same container
// engine resources, so it fails fast instead.
const ErrAlreadyRunning = Error("runstate: already running")

// ErrNotRunning reports that no run is tracked for this project and
// environment.
const ErrNotRunning = Error("runstate: not running")

// ErrStaleRun reports that the tracked run is gone; its leftover state was
// removed.
const ErrStaleRun = Error("runstate: not running (removed stale pid file)")
