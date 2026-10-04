package netca

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrCAFile reports that the extra roots could not be set up: the file
	// named by KEVIN_PLUGIN_CA_FILE is unreadable or holds no PEM
	// certificate, or the system roots or default transport are unusable.
	ErrCAFile = Error("netca: cannot use the plugin CA file")
)
