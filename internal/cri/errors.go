package cri

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNotFound reports that a container, an image, or a network is absent.
	ErrNotFound = Error("cri: no such object")

	// ErrUnavailable reports that the engine command is absent, or that the
	// daemon does not answer.
	ErrUnavailable = Error("cri: the engine command or the daemon is unavailable")

	// ErrNoGateway reports that a network carries no IPv4 gateway address.
	ErrNoGateway = Error("cri: the network reports no ipv4 gateway")
)
