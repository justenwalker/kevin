package plugin

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrUnknownStepType reports an Up or a Down request for a step type
	// that the provider does not offer.
	ErrUnknownStepType = Error("plugin: unknown step type")

	// ErrConfigureNotSupported reports a Configure call that carries a
	// config block, sent to a provider whose Configure is nil. Without this
	// check the config would be silently discarded.
	ErrConfigureNotSupported = Error("plugin: provider takes no configuration")
)
