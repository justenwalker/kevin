package wait

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

const (
	// ErrCheck reports that a step's with block set zero, or more than one,
	// of tcp, http, kubectl, exec, duration.
	ErrCheck = Error("wait: with must set exactly one of tcp, http, kubectl, exec, duration")

	// ErrKubectlMode reports that a kubectl check set zero, or both, of
	// for and rollout.
	ErrKubectlMode = Error("wait: kubectl check must set exactly one of for, rollout")

	// ErrTimeout reports that the check did not succeed before the
	// timeout passed.
	ErrTimeout = Error("wait: timed out waiting for the check to succeed")

	// ErrStatus reports that an http check's response status did not
	// match.
	ErrStatus = Error("wait: http probe returned an unexpected status")
)
