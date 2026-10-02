package git

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the git command is absent.
const ErrUnavailable = Error("git: the git command is unavailable")
