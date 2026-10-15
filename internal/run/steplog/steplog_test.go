package steplog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLog writes lines, one NDJSON object per entry, to a fresh file
// under t.TempDir and returns its path - the same shape
// internal/engine/ndjsonlog.go's slog.JSONHandler produces.
func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "logs.ndjson")
	content := strings.Join(lines, "\n") + "\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

func TestReadSince(t *testing.T) {
	t.Run("reads every entry from the start with a zero cursor", func(t *testing.T) {
		path := writeLog(t,
			`{"time":"2026-01-01T00:00:00Z","msg":"hello","step":"web","stream":"stdout"}`,
			`{"time":"2026-01-01T00:00:01Z","msg":"world","step":"web","stream":"stdout"}`,
		)

		entries, cursor, err := ReadSince(path, "", "")
		require.NoError(t, err)
		require.Len(t, entries, 2)
		assert.Equal(t, "hello", entries[0].Text)
		assert.Equal(t, "world", entries[1].Text)
		assert.NotEmpty(t, cursor)
	})

	t.Run("filters to one step", func(t *testing.T) {
		path := writeLog(t,
			`{"time":"2026-01-01T00:00:00Z","msg":"a","step":"web","stream":"stdout"}`,
			`{"time":"2026-01-01T00:00:01Z","msg":"b","step":"db","stream":"stdout"}`,
		)

		entries, _, err := ReadSince(path, "db", "")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "b", entries[0].Text)
	})

	t.Run("a later call with the returned cursor sees only what was appended after", func(t *testing.T) {
		path := writeLog(t, `{"time":"2026-01-01T00:00:00Z","msg":"first","step":"web","stream":"stdout"}`)

		_, cursor, err := ReadSince(path, "", "")
		require.NoError(t, err)

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(`{"time":"2026-01-01T00:00:01Z","msg":"second","step":"web","stream":"stdout"}` + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())

		entries, _, err := ReadSince(path, "", cursor)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "second", entries[0].Text)
	})

	t.Run("stops cleanly at a partial trailing line without consuming it", func(t *testing.T) {
		path := writeLog(t, `{"time":"2026-01-01T00:00:00Z","msg":"complete","step":"web","stream":"stdout"}`)

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		_, err = f.WriteString(`{"time":"2026-01-01T00:00:01Z","msg":"partial`) // no closing brace/quote, no newline
		require.NoError(t, err)
		require.NoError(t, f.Close())

		entries, cursor, err := ReadSince(path, "", "")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "complete", entries[0].Text)

		// Calling again with the same cursor still sees nothing new - the
		// partial line was never consumed.
		entries, _, err = ReadSince(path, "", cursor)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})

	t.Run("reports ErrNotFound for a missing log file", func(t *testing.T) {
		_, _, err := ReadSince(filepath.Join(t.TempDir(), "missing.ndjson"), "", "")
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("reports ErrInvalidCursor for a malformed cursor", func(t *testing.T) {
		path := writeLog(t, `{"time":"2026-01-01T00:00:00Z","msg":"a","step":"web","stream":"stdout"}`)

		_, _, err := ReadSince(path, "", "not-a-cursor")
		require.ErrorIs(t, err, ErrInvalidCursor)
	})

	t.Run("reports ErrStaleCursor for a cursor past the current file's end", func(t *testing.T) {
		longPath := writeLog(t,
			`{"time":"2026-01-01T00:00:00Z","msg":"a","step":"web","stream":"stdout"}`,
			`{"time":"2026-01-01T00:00:01Z","msg":"b","step":"web","stream":"stdout"}`,
		)
		_, cursor, err := ReadSince(longPath, "", "")
		require.NoError(t, err)

		shortPath := writeLog(t, `{"time":"2026-01-01T00:00:00Z","msg":"a","step":"web","stream":"stdout"}`)
		_, _, err = ReadSince(shortPath, "", cursor)
		require.ErrorIs(t, err, ErrStaleCursor)
	})
}
