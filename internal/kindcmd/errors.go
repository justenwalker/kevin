package kindcmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the kind command is absent.
const ErrUnavailable = Error("kindcmd: the kind command is unavailable")
