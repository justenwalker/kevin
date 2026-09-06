package main

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNoAddress reports that no network interface carries a usable
	// address for -self.
	ErrNoAddress = Error("relay: no usable address found on the network interfaces")

	// ErrNoSNI reports that a TLS ClientHello carries no server name.
	ErrNoSNI = Error("relay: the client hello carries no server name")

	// ErrTruncated reports that a TLS record ended before the parser
	// reached the end of the structure it was reading.
	ErrTruncated = Error("relay: the tls record is truncated")

	// ErrNotHandshake reports that a TLS record, or a handshake message
	// inside it, is not a ClientHello.
	ErrNotHandshake = Error("relay: the record is not a tls client hello")

	// ErrProxyRejected reports that the host proxy did not answer a CONNECT
	// request with a 200 status.
	ErrProxyRejected = Error("relay: the proxy rejected the connect request")

	// ErrInvalidControlClientCA reports that the client CA material the
	// engine embedded in the environment carries no usable certificate.
	ErrInvalidControlClientCA = Error("relay: the control client ca is invalid")

	// ErrCaptureUnsupported reports that this relay build cannot install
	// transparent-capture rules - only a linux build can.
	ErrCaptureUnsupported = Error("relay: transparent capture requires linux")

	// ErrOrigDstUnsupported reports that this relay build cannot read a
	// connection's pre-NAT destination - only a linux build can.
	ErrOrigDstUnsupported = Error("relay: reading the original destination requires linux")
)
