package fault

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNoImpairment reports that a step's with block set no impairment
	// field, which would be a meaningless no-op.
	ErrNoImpairment = Error("fault: with must set at least one impairment field")

	// ErrNoContainers reports that none of this step's needs entries
	// report any containers at all - nothing for an unset containers
	// field to default to.
	ErrNoContainers = Error("fault: no needs step reports any containers")

	// ErrContainerNotFound reports that a containers entry names no
	// container (and no single-container needs step) that this step's
	// needs actually resolve to.
	ErrContainerNotFound = Error("fault: containers names a container no needs step reports")
)
