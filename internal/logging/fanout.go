package logging

import (
	"context"
	"errors"
	"log/slog"
)

// fanoutHandler sends every record to each of its handlers.
type fanoutHandler []slog.Handler

// Fanout returns an [slog.Handler] that forwards each record to every handler
// in handlers that has it enabled.
func Fanout(handlers ...slog.Handler) slog.Handler {
	return fanoutHandler(handlers)
}

// Enabled implements [slog.Handler].
func (f fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

// Handle implements [slog.Handler].
func (f fanoutHandler) Handle(ctx context.Context, r slog.Record) error {
	var errs []error
	for _, h := range f {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WithAttrs implements [slog.Handler].
func (f fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make(fanoutHandler, len(f))
	for i, h := range f {
		next[i] = h.WithAttrs(attrs)
	}
	return next
}

// WithGroup implements [slog.Handler].
func (f fanoutHandler) WithGroup(name string) slog.Handler {
	next := make(fanoutHandler, len(f))
	for i, h := range f {
		next[i] = h.WithGroup(name)
	}
	return next
}
