package k3dcmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the k3d command is absent.
const ErrUnavailable = Error("k3dcmd: the k3d command is unavailable")
