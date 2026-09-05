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

// ErrNoControlAddr reports that the relay container publishes no address
// for its intercept control endpoint.
const ErrNoControlAddr = Error("relay: the relay container publishes no control address")

// ErrUnsupportedControlKey reports that a minted control-channel
// certificate's private key is not ECDSA - every leaf [ca.CA.NewLeaf]
// mints is, so this should not happen.
const ErrUnsupportedControlKey = Error("relay: the control certificate's key is not ecdsa")
