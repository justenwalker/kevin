package pluginindex

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrAliasTaken reports that AddSource's alias already names a
	// different source.
	ErrAliasTaken = Error("pluginindex: alias is already in use")

	// ErrUnknownSource reports a RemoveSource target that matches no
	// configured source.
	ErrUnknownSource = Error("pluginindex: no such index source")

	// ErrVersionMismatch reports a versions/<semver>.yaml file whose
	// "version" field does not match its own filename stem.
	ErrVersionMismatch = Error("pluginindex: version field does not match filename")

	// ErrAmbiguousPlugin reports a bare name that matches a plugin in more
	// than one configured source.
	ErrAmbiguousPlugin = Error("pluginindex: name matches more than one plugin, use <alias>/<name>")

	// ErrPluginNotFound reports a name or alias/name that matches no
	// plugin in any configured source.
	ErrPluginNotFound = Error("pluginindex: no such plugin")

	// ErrVersionNotFound reports a Resolve --version that matches no
	// version of the resolved plugin.
	ErrVersionNotFound = Error("pluginindex: no such version")

	// ErrBadSigner reports a plugin.yaml signers entry whose minisign key
	// does not parse.
	ErrBadSigner = Error("pluginindex: invalid signer")

	// ErrVersionSignatureMissing reports a version file loaded from a
	// plugin's VersionSource with no ".minisig" or ".sigstore.json"
	// sibling.
	ErrVersionSignatureMissing = Error("pluginindex: federated version has no detached signature")

	// ErrVersionSignatureInvalid reports a version file loaded from a
	// plugin's VersionSource whose detached signature does not verify
	// against any of the plugin's own declared signers.
	ErrVersionSignatureInvalid = Error("pluginindex: federated version's signature does not verify")

	// ErrVersionSourceNoSigners reports a plugin.yaml that sets
	// version_source but declares no signers at all - nothing could ever
	// verify a version loaded from it.
	ErrVersionSourceNoSigners = Error("pluginindex: version_source is set but no signers are declared")

	// ErrIndexMarkerMissing reports a cloned repo (an index Source, or a
	// plugin's VersionSource) with no kevin-index.yaml at its root - not
	// shaped like a kevin plugin index at all.
	ErrIndexMarkerMissing = Error("pluginindex: not a kevin plugin index (no kevin-index.yaml)")
)
