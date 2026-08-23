package session

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrStepBusy reports that a step's Up is already running - either the
// initial bring-up hasn't reached it yet, or an earlier rerun is still in
// flight - so a new rerun request for it is rejected rather than queued.
const ErrStepBusy = Error("session: step is already running")
