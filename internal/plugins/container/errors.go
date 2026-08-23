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
)
