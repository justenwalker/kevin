package engines

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnsupported reports that name is not an engine kevin implements.
const ErrUnsupported = Error("engines: unsupported engine")

// ErrNoEngine reports that Detect found neither docker nor podman reachable.
const ErrNoEngine = Error("engines: no container engine found (checked docker, podman)")
