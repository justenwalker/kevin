package proxy

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNoSigningCertificate reports that the authority handed to New
	// carries no certificate to sign leaves with.
	ErrNoSigningCertificate = Error("proxy: the authority has no signing certificate")

	// ErrUnsupportedSigningKey reports a signing key that is not ECDSA -
	// every authority internal/ca issues is, so this should not happen
	// outside a test double.
	ErrUnsupportedSigningKey = Error("proxy: the authority's signing key is not ECDSA")
)
