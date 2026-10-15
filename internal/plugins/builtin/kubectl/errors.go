package kubectl

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrSource reports that a step's with block set zero, or more than one, of
// manifest, path, and kustomize.
const ErrSource = Error("kubectl: with must set exactly one of manifest, path, kustomize")
