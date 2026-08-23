package pkgtrust

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrBadKey reports a file that does not parse as a minisign public key.
	ErrBadKey = Error("pkgtrust: not a valid minisign public key")

	// ErrUnknownKeyID reports a signature's key id, or a Remove target's key
	// id, that matches no key in the trust store.
	ErrUnknownKeyID = Error("pkgtrust: no such key in the trust store")

	// ErrSignatureInvalid reports a signature that a trusted key does not
	// verify.
	ErrSignatureInvalid = Error("pkgtrust: signature verification failed")

	// ErrSignatureMissing reports a plugins.<name> entry with signed: true
	// whose package has no detached signature to verify.
	ErrSignatureMissing = Error("pkgtrust: signed: true but the package has no signature")
)
