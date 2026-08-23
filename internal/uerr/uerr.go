// Package uerr wraps an error with a message meant for a human reader,
// while keeping the technical chain intact for logs and errors.Is/As.
package uerr

import (
	"errors"
	"fmt"
	"strings"
)

// Error pairs a wrapped error with a message meant for a human. The
// message is kept as a format string plus its args, not pre-formatted, so
// format stays stable text a future translation catalog can key on. The
// zero value is not usable. Construct one with [Wrap] or [WrapText].
type Error struct {
	format string
	args   []string
	err    error
}

// Wrap attaches a human-facing message to err, built from format and a
// the way fmt.Sprintf would build it - except every arg is stringified
// with fmt.Sprint first, so format's verbs should be %s. That keeps
// [Error.Message] identical whether it runs here or after [WrapText]
// rebuilds the same Error from args that crossed a process boundary as
// plain strings. Wrap returns nil if err is nil. Wrapping an error that
// already carries a human message adds another layer; [Display] joins
// every layer found in the chain, outermost first.
func Wrap(err error, format string, a ...any) error {
	if err == nil {
		return nil
	}
	var args []string
	for _, v := range a {
		args = append(args, fmt.Sprint(v))
	}
	return &Error{format: format, args: args, err: err}
}

// WrapText attaches a human-facing message to err using key and args in
// their already-stringified form. Use this to reconstruct an [Error] that
// crossed a process boundary. WrapText returns nil if err is nil.
func WrapText(err error, key string, args ...string) error {
	if err == nil {
		return nil
	}
	return &Error{format: key, args: args, err: err}
}

// Message formats e's own human-facing text (not the rest of the chain).
func (e *Error) Message() string {
	a := make([]any, len(e.args))
	for i, v := range e.args {
		a[i] = v
	}
	return fmt.Sprintf(e.format, a...)
}

// Format returns the format string e was built with, unformatted - a
// candidate message key for a future translation catalog.
func (e *Error) Format() string { return e.format }

// Args returns e's format arguments, each in its plain text form, in the
// order Format's verbs expect them - what a future translation catalog
// would interpolate into a localized template keyed on Format.
func (e *Error) Args() []string { return e.args }

// Error returns the wrapped error's message unchanged, so a caller that
// only cares about the technical chain (logs, errors.Is/As) sees no
// difference from an error that was never wrapped.
func (e *Error) Error() string { return e.err.Error() }

// Unwrap returns the wrapped error.
func (e *Error) Unwrap() error { return e.err }

// Display returns err's human-facing text: every message attached via
// Wrap in the chain, outermost first, joined with ": ". If err carries no
// human message, Display falls back to err.Error(). Display returns "" for
// a nil err.
func Display(err error) string {
	if err == nil {
		return ""
	}
	var parts []string
	for {
		var ue *Error
		if !errors.As(err, &ue) {
			break
		}
		parts = append(parts, ue.Message())
		err = ue.err
	}
	if parts == nil {
		return err.Error()
	}
	return strings.Join(parts, ": ")
}
