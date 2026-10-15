package signing

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrBadKey reports a file that does not parse as a minisign public key.
	ErrBadKey = Error("signing: not a valid minisign public key")

	// ErrUnknownKeyID reports a signature's key id, or a Remove target's key
	// id, that matches no key in the trust store.
	ErrUnknownKeyID = Error("signing: no such key in the trust store")

	// ErrSignatureInvalid reports a signature that a trusted key does not
	// verify.
	ErrSignatureInvalid = Error("signing: signature verification failed")

	// ErrSignatureMissing reports a plugins.<name> entry with a signing
	// block whose package has no detached signature to verify.
	ErrSignatureMissing = Error("signing: signing is set but the package has no signature")

	// ErrIdentityUntrusted reports a sigstore identity/issuer pair, or a
	// RemoveIdentity target, that matches no entry in the trust store.
	ErrIdentityUntrusted = Error("signing: identity is not trusted")
)
