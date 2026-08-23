package plugin

import "github.com/justenwalker/kevin/internal/uerr"

// Wrap attaches a human-facing message to err, built from format and args
// the way fmt.Sprintf would build it. A Step returns the wrapped error
// from Up, Down, or Export so the supervisor shows that message - in the
// console, the TUI, and the CLI - instead of err's raw chain. Wrap returns
// nil if err is nil.
func Wrap(err error, format string, a ...any) error {
	return uerr.Wrap(err, format, a...)
}
