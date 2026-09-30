package podman

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNoSocket reports that podman named no API socket for a tool that talks
// the Docker API.
const ErrNoSocket = Error("podman: no API socket")
