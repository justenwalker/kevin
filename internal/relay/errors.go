package relay

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNoAddress reports that the relay container carries no address on the
// shared network.
const ErrNoAddress = Error("relay: the relay container has no address on the network")
