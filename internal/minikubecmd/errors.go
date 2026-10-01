package minikubecmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the minikube command is absent.
const ErrUnavailable = Error("minikubecmd: the minikube command is unavailable")
