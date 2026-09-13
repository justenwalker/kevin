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
