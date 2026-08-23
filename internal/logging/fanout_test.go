package logging_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/logging"
)

// stubHandler is a minimal slog.Handler whose Handle always returns err, for
// proving fanoutHandler.Handle joins every failing handler's error.
type stubHandler struct {
	enabled bool
	err     error
}

func (s stubHandler) Enabled(context.Context, slog.Level) bool  { return s.enabled }
func (s stubHandler) Handle(context.Context, slog.Record) error { return s.err }
func (s stubHandler) WithAttrs([]slog.Attr) slog.Handler        { return s }
func (s stubHandler) WithGroup(string) slog.Handler             { return s }

func newRecord() slog.Record {
	return slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
}

func TestFanout(t *testing.T) {
	t.Run("Enabled is true when any handler has the level enabled", func(t *testing.T) {
		f := logging.Fanout(stubHandler{enabled: false}, stubHandler{enabled: true})
		assert.True(t, f.Enabled(t.Context(), slog.LevelInfo))
	})

	t.Run("Enabled is false when no handler has the level enabled", func(t *testing.T) {
		f := logging.Fanout(stubHandler{enabled: false}, stubHandler{enabled: false})
		assert.False(t, f.Enabled(t.Context(), slog.LevelInfo))
	})

	t.Run("Handle forwards the record to every enabled handler", func(t *testing.T) {
		var buf1, buf2 bytes.Buffer
		f := logging.Fanout(
			logging.NewHuman(&buf1, slog.LevelInfo, false),
			logging.NewHuman(&buf2, slog.LevelInfo, false),
		)
		require.NoError(t, f.Handle(t.Context(), newRecord()))
		assert.NotEmpty(t, buf1.String())
		assert.NotEmpty(t, buf2.String())
	})

	t.Run("Handle skips a handler that has the record's level disabled", func(t *testing.T) {
		var buf bytes.Buffer
		f := logging.Fanout(logging.NewHuman(&buf, slog.LevelError, false))
		require.NoError(t, f.Handle(t.Context(), newRecord()))
		assert.Empty(t, buf.String(), "a record below the handler's level must never reach Handle")
	})

	t.Run("Handle joins every failing handler's error", func(t *testing.T) {
		errA := errors.New("a failed")
		errB := errors.New("b failed")
		f := logging.Fanout(
			stubHandler{enabled: true, err: errA},
			stubHandler{enabled: true, err: errB},
		)

		err := f.Handle(t.Context(), newRecord())
		require.Error(t, err)
		require.ErrorIs(t, err, errA)
		require.ErrorIs(t, err, errB)
	})

	t.Run("WithAttrs applies to every handler", func(t *testing.T) {
		var buf bytes.Buffer
		f := logging.Fanout(logging.NewHuman(&buf, slog.LevelInfo, false)).
			WithAttrs([]slog.Attr{slog.String("step", "api")})

		require.NoError(t, f.Handle(t.Context(), newRecord()))
		assert.Contains(t, buf.String(), "step=api")
	})

	t.Run("WithGroup applies to every handler", func(t *testing.T) {
		var buf bytes.Buffer
		f := logging.Fanout(logging.NewHuman(&buf, slog.LevelInfo, false)).
			WithGroup("http").
			WithAttrs([]slog.Attr{slog.String("status", "200")})

		require.NoError(t, f.Handle(t.Context(), newRecord()))
		assert.Contains(t, buf.String(), "http.status=200")
	})
}
