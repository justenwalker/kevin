package httppkg

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrBadURL reports a rawURL that does not parse, or does not name an
	// http or https scheme.
	ErrBadURL = Error("httppkg: not a valid http(s) url")

	// ErrFetch reports that the URL could not be reached, or answered with
	// a non-2xx status.
	ErrFetch = Error("httppkg: could not fetch the url")

	// ErrChecksumMismatch reports a downloaded package whose bytes do not
	// match the checksum kevin.cue pinned.
	ErrChecksumMismatch = Error("httppkg: package does not match the expected checksum")
)
