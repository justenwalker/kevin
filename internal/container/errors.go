package container

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNotFound reports that a container, an image, or a network is absent.
	ErrNotFound = Error("container: no such object")

	// ErrUnavailable reports that the engine command is absent, or that the
	// daemon does not answer.
	ErrUnavailable = Error("container: the engine command or the daemon is unavailable")

	// ErrNoGateway reports that a network carries no IPv4 gateway address.
	ErrNoGateway = Error("container: the network reports no ipv4 gateway")
)
