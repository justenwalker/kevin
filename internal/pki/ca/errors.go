package ca

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrNotFound reports that the directory holds no authority.
	ErrNotFound = Error("ca: no certificate authority in the directory")

	// ErrInvalid reports that the files in the directory are not a usable
	// authority.
	ErrInvalid = Error("ca: the certificate authority is not usable")
)
