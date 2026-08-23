package ocipkg

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrBadReference reports a ref that does not parse as
	// HOST[:PORT]/REPO[:TAG|@DIGEST], or names neither a tag nor a digest.
	ErrBadReference = Error("ocipkg: not a valid oci reference")

	// ErrFetch reports that the registry could not be reached, or refused
	// the request: network failure, authentication, or an unknown
	// repository/tag/digest.
	ErrFetch = Error("ocipkg: could not fetch from the registry")

	// ErrNoMatchingPlatform reports a multi-arch index with no manifest for
	// this host's OS/architecture.
	ErrNoMatchingPlatform = Error("ocipkg: image index has no manifest for this platform")

	// ErrMediaType reports a manifest, or its layer, that is not a kevin
	// plugin package.
	ErrMediaType = Error("ocipkg: manifest does not describe a kevin plugin package")

	// ErrBlobMismatch reports a fetched blob whose bytes do not match the
	// digest its manifest declared.
	ErrBlobMismatch = Error("ocipkg: fetched blob does not match its declared digest")

	// ErrPush reports that the registry refused, or could not be reached
	// for, a blob or manifest push.
	ErrPush = Error("ocipkg: could not push to the registry")
)
