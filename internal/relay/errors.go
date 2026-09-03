package relay

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrNoAddress reports that the relay container carries no address on the
// shared network.
const ErrNoAddress = Error("relay: the relay container has no address on the network")

// ErrNoSOCKS5Addr reports that the relay container publishes no address
// for its SOCKS5 gateway.
const ErrNoSOCKS5Addr = Error("relay: the relay container publishes no socks5 address")
