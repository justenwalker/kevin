package cache

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrDigestMismatch reports content whose sha256 is not the digest the
	// caller expected.
	ErrDigestMismatch = Error("cache: content does not match the expected digest")
)
