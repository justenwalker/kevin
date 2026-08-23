package pluginhost

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNoResult reports that the Up stream of a plugin ended and published no
	// result.
	ErrNoResult = Error("pluginhost: plugin ended Up without a result")

	// ErrNameMismatch reports that the name of a plugin differs from the key
	// that the configuration uses for it.
	ErrNameMismatch = Error("pluginhost: plugin name does not match its configuration key")

	// ErrCrashed reports that a plugin process is no longer responding -
	// typically because it crashed. An RPC error wraps this when the gRPC
	// transport itself failed, as opposed to the plugin returning an
	// ordinary application-level error.
	ErrCrashed = Error("pluginhost: the plugin process crashed or is unresponsive")
)
