package gitcmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the git command is absent.
const ErrUnavailable = Error("gitcmd: the git command is unavailable")
