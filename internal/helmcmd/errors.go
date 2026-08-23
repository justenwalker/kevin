package helmcmd

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrUnavailable reports that the helm command is absent.
const ErrUnavailable = Error("helmcmd: the helm command is unavailable")

// ErrReleaseNotFound reports that the release named in a call is already
// gone. Uninstall reports it instead of helm's own exit error, so a caller
// can treat a missing release as done rather than failed.
const ErrReleaseNotFound = Error("helmcmd: release not found")
