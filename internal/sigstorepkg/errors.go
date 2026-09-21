package sigstorepkg

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrCosignNotFound reports that the cosign command is absent from PATH.
const ErrCosignNotFound = Error("sigstorepkg: cosign is not installed, or not on PATH")

// ErrVerifyFailed reports that cosign verify-blob rejected the package.
// cosign verify-blob has no stable machine-readable failure taxonomy, so
// this one sentinel covers identity mismatch, issuer mismatch, a bad
// certificate chain, and a bad Rekor proof alike - the wrapped error
// carries cosign's own stderr for a human to read.
const ErrVerifyFailed = Error("sigstorepkg: cosign verify-blob failed")
