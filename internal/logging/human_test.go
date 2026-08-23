package logging_test

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/logging"
)

// fixedRecord builds a record at a fixed time, so the leading timestamp in a
// rendered line is predictable.
func fixedRecord(msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC), slog.LevelInfo, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

func TestHumanHandler(t *testing.T) {
	t.Run("writes a short aligned line", func(t *testing.T) {
		var buf bytes.Buffer
		h := logging.NewHuman(&buf, slog.LevelInfo, false)
		require.NoError(t, h.Handle(t.Context(), fixedRecord("hello", slog.String("key", "value"))))
		assert.Equal(t, "15:04:05 INFO  hello key=value\n", buf.String())
	})

	t.Run("quotes a value that needs it", func(t *testing.T) {
		tests := []struct {
			name string
			val  string
			want string
		}{
			{name: "plain", val: "value", want: "value"},
			{name: "space", val: "has space", want: `"has space"`},
			{name: "tab", val: "has\ttab", want: "\"has\\ttab\""},
			{name: "quote", val: `has"quote`, want: `"has\"quote"`},
			{name: "empty", val: "", want: `""`},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var buf bytes.Buffer
				h := logging.NewHuman(&buf, slog.LevelInfo, false)
				require.NoError(t, h.Handle(t.Context(), fixedRecord("m", slog.String("k", tt.val))))
				assert.Contains(t, buf.String(), "k="+tt.want)
			})
		}
	})

	t.Run("colors the level when color is true", func(t *testing.T) {
		var buf bytes.Buffer
		h := logging.NewHuman(&buf, slog.LevelInfo, true)
		require.NoError(t, h.Handle(t.Context(), fixedRecord("m")))
		assert.Contains(t, buf.String(), "\x1b[36mINFO \x1b[0m")
	})

	t.Run("Enabled filters by level", func(t *testing.T) {
		h := logging.NewHuman(&bytes.Buffer{}, slog.LevelWarn, false)
		assert.False(t, h.Enabled(t.Context(), slog.LevelInfo))
		assert.True(t, h.Enabled(t.Context(), slog.LevelWarn))
	})

	t.Run("WithGroup prefixes later attrs with a dotted group name", func(t *testing.T) {
		var buf bytes.Buffer
		h := logging.NewHuman(&buf, slog.LevelInfo, false).WithGroup("http")
		require.NoError(t, h.Handle(t.Context(), fixedRecord("m", slog.String("status", "200"))))
		assert.Contains(t, buf.String(), "http.status=200")
	})

	t.Run("WithGroup with an empty name is a no-op", func(t *testing.T) {
		h := logging.NewHuman(&bytes.Buffer{}, slog.LevelInfo, false)
		assert.Same(t, h, h.WithGroup(""))
	})

	t.Run("WithAttrs with no attrs is a no-op", func(t *testing.T) {
		h := logging.NewHuman(&bytes.Buffer{}, slog.LevelInfo, false)
		assert.Same(t, h, h.WithAttrs(nil))
	})

	t.Run("WithAttrs returns a copy, leaving the receiver unaffected", func(t *testing.T) {
		var buf bytes.Buffer
		base := logging.NewHuman(&buf, slog.LevelInfo, false)
		withStep := base.WithAttrs([]slog.Attr{slog.String("step", "api")})

		require.NoError(t, base.Handle(t.Context(), fixedRecord("m")))
		assert.NotContains(t, buf.String(), "step=", "the base handler must not gain the copy's attrs")

		buf.Reset()
		require.NoError(t, withStep.Handle(t.Context(), fixedRecord("m")))
		assert.Contains(t, buf.String(), "step=api")
	})
}
