package cmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnknownPlugin reports a "plugin run" request for a name that names no
// provider that ships inside kevin.
const ErrUnknownPlugin = Error("cmd: unknown plugin")

// ErrAlreadyRunning reports that "kevin run" found a live pidfile for this
// project and environment - a second run against the same one would race
// the same Docker resources instead of failing fast.
const ErrAlreadyRunning = Error("cmd: run: already running")

// ErrIndexUpdateFailed reports that "plugin index update" failed to clone
// or load at least one configured source - every source was still
// attempted, and its own failure is printed on its own line.
const ErrIndexUpdateFailed = Error("cmd: index update: at least one source failed")

// ErrNotRunning reports that no "kevin run" is tracked for this project and
// environment.
const ErrNotRunning = Error("cmd: not running")

// ErrStaleRun reports that the tracked "kevin run" is gone; its leftover pid
// file was removed.
const ErrStaleRun = Error("cmd: not running (removed stale pid file)")
