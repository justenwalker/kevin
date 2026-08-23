package kubectlcmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the kubectl command is absent.
const ErrUnavailable = Error("kubectlcmd: the kubectl command is unavailable")
