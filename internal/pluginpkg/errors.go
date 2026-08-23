package pluginpkg

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrChecksumMismatch reports that a package's bytes do not match the
	// checksum the caller expected.
	ErrChecksumMismatch = Error("pluginpkg: package does not match the expected checksum")

	// ErrBadArchive reports that a package is not a readable tar (optionally
	// gzip-compressed) archive.
	ErrBadArchive = Error("pluginpkg: not a valid tar archive")

	// ErrUnsafePath reports an archive entry, or a manifest entrypoint, that
	// would resolve outside the destination directory.
	ErrUnsafePath = Error("pluginpkg: archive entry escapes the destination directory")

	// ErrManifestMissing reports that a package has no manifest.json.
	ErrManifestMissing = Error("pluginpkg: package has no manifest.json")

	// ErrManifestInvalid reports that manifest.json does not decode, or is
	// missing a required field.
	ErrManifestInvalid = Error("pluginpkg: manifest.json is invalid")

	// ErrManifestVersion reports a manifest.json whose $v this package does
	// not understand.
	ErrManifestVersion = Error("pluginpkg: manifest.json declares an unsupported $v")

	// ErrEntrypointMissing reports that the manifest names an entrypoint the
	// package does not contain.
	ErrEntrypointMissing = Error("pluginpkg: manifest names an entrypoint the package does not contain")
)
