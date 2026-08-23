package cmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnknownPlugin reports a "plugin run" request for a name that names no
// provider that ships inside kevin.
const ErrUnknownPlugin = Error("cmd: unknown plugin")
