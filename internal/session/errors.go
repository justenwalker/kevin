package session

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrStepBusy reports that a step's Up is already running - either the
// initial bring-up hasn't reached it yet, or an earlier rerun is still in
// flight - so a new rerun request for it is rejected rather than queued.
const ErrStepBusy = Error("session: step is already running")

// ErrStepTimeout reports that a step's Up outlived the step's timeout.
const ErrStepTimeout = Error("session: step exceeded its timeout")

// ErrUnknownStep reports that a rerun named a step the environment does not
// declare.
const ErrUnknownStep = Error("session: no such step")
