package pkg

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrChecksumMismatch reports that a package's bytes do not match the
	// checksum the caller expected.
	ErrChecksumMismatch = Error("pkg: package does not match the expected checksum")

	// ErrBadArchive reports that a package is not a readable tar (optionally
	// gzip-compressed) archive.
	ErrBadArchive = Error("pkg: not a valid tar archive")

	// ErrUnsafePath reports an archive entry, or a manifest entrypoint, that
	// would resolve outside the destination directory.
	ErrUnsafePath = Error("pkg: archive entry escapes the destination directory")

	// ErrManifestMissing reports that a package has no manifest.json.
	ErrManifestMissing = Error("pkg: package has no manifest.json")

	// ErrManifestInvalid reports that manifest.json does not decode, or is
	// missing a required field.
	ErrManifestInvalid = Error("pkg: manifest.json is invalid")

	// ErrManifestVersion reports a manifest.json whose $v this package does
	// not understand.
	ErrManifestVersion = Error("pkg: manifest.json declares an unsupported $v")

	// ErrEntrypointMissing reports that the manifest names an entrypoint the
	// package does not contain.
	ErrEntrypointMissing = Error("pkg: manifest names an entrypoint the package does not contain")
)
