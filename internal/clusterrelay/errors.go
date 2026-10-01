package clusterrelay

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNoPublishedPort reports that the forwarder container publishes no
	// host port for the relay's SOCKS5 gateway.
	ErrNoPublishedPort = Error("clusterrelay: the forwarder publishes no socks5 port")
)
