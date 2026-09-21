package container

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrExited reports that the container stopped before it was ready.
	ErrExited = Error("container: the container stopped during startup")

	// ErrNoPort reports that an expose entry names a port the container
	// does not publish.
	ErrNoPort = Error("container: the exposed port is not published")

	// ErrUnsupportedEngine reports that Env.Engine names an engine this
	// plugin does not implement.
	ErrUnsupportedEngine = Error("container: unsupported engine")

	// ErrNoRelay reports that an expose entry sets relay, but no relay
	// address is available.
	ErrNoRelay = Error("container: expose relay: no relay address available")

	// ErrNoRelayUDPPool reports that a relay+udp expose entry has no UDP
	// relay pool to draw from - the relay publishes none, typically
	// because KEVIN_RELAY_UDP_POOL_SIZE is set to 0.
	ErrNoRelayUDPPool = Error("container: expose relay: no udp relay pool available")

	// ErrNotRunning reports that Export found the container, but it isn't
	// running.
	ErrNotRunning = Error("container: the container is not running")
)
