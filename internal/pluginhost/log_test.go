package pluginhost

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlogWriter(t *testing.T) {
	tests := []struct {
		name      string
		writes    []string
		wantLines []string
	}{
		{
			name:      "a single write ending in a newline emits one line",
			writes:    []string{"hello\n"},
			wantLines: []string{"hello"},
		},
		{
			name:      "a write with no newline buffers until one arrives",
			writes:    []string{"hel", "lo\n"},
			wantLines: []string{"hello"},
		},
		{
			name:      "a write with no trailing newline emits nothing yet",
			writes:    []string{"partial"},
			wantLines: nil,
		},
		{
			name:      "one write carries multiple lines",
			writes:    []string{"first\nsecond\nthird\n"},
			wantLines: []string{"first", "second", "third"},
		},
		{
			name:      "a trailing partial line after a complete one stays buffered",
			writes:    []string{"done\nnot yet"},
			wantLines: []string{"done"},
		},
		{
			name:      "an empty write is a no-op",
			writes:    []string{""},
			wantLines: nil,
		},
		{
			name:      "a carriage return before the newline is trimmed too",
			writes:    []string{"windows style\r\n"},
			wantLines: []string{"windows style"},
		},
		{
			name:      "many small writes assemble one line",
			writes:    []string{"a", "b", "c", "\n"},
			wantLines: []string{"abc"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("plugin", "echo")

			w := &slogWriter{logger: logger}
			for _, chunk := range tt.writes {
				n, err := w.Write([]byte(chunk))
				require.NoError(t, err)
				assert.Equal(t, len(chunk), n, "Write must report every byte consumed, even a buffered partial line")
			}

			var gotLines []string
			for raw := range strings.SplitSeq(strings.TrimRight(buf.String(), "\n"), "\n") {
				if raw == "" {
					continue
				}
				var record map[string]any
				require.NoError(t, json.Unmarshal([]byte(raw), &record))
				assert.Equal(t, "echo", record["plugin"], "every line must carry the plugin name")
				msg, ok := record["msg"].(string)
				require.True(t, ok, "record must carry a string msg field")
				gotLines = append(gotLines, msg)
			}
			assert.Equal(t, tt.wantLines, gotLines)
		})
	}

	t.Run("caps an unterminated line", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		w := &slogWriter{logger: logger}

		long := strings.Repeat("a", maxLineLength+1)
		n, err := w.Write([]byte(long))
		require.NoError(t, err)
		assert.Equal(t, len(long), n)

		lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
		require.Len(t, lines, 1, "crossing the cap must flush once, not buffer forever")

		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &record))
		msg, _ := record["msg"].(string)
		assert.True(t, strings.HasPrefix(msg, long), "the buffered bytes must not be dropped")
		assert.True(t, strings.HasSuffix(msg, "..."), "the message must mark where the line was cut off")

		buf.Reset()
		n, err = w.Write([]byte("next\n"))
		require.NoError(t, err)
		assert.Equal(t, 5, n)

		require.NoError(t, json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &record))
		assert.Equal(t, "next", record["msg"], "the writer must keep working normally after a forced flush")
	})
}

func TestHCLogToSlog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})).With("plugin", "echo")

	hlog := hclog.New(&hclog.LoggerOptions{
		Level:  hclog.Debug,
		Output: &slogWriter{logger: logger},
	})

	hlog.Debug("starting up", "attempt", 1)
	hlog.Warn("retrying")

	var records []map[string]any
	for raw := range strings.SplitSeq(strings.TrimRight(buf.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &record))
		records = append(records, record)
	}
	require.Len(t, records, 2, "each hclog call writes one complete line")

	assert.Contains(t, records[0]["msg"], "starting up")
	assert.Contains(t, records[0]["msg"], "attempt=1", "hclog's own key=value formatting must survive")
	assert.Equal(t, "echo", records[0]["plugin"])

	assert.Contains(t, records[1]["msg"], "retrying")

	for i, record := range records {
		assert.Equal(t, "DEBUG", record["level"], "line %d: slogWriter forwards every line at slog debug, regardless of hclog's own level", i)
	}
}

func TestSlogWriterClose(t *testing.T) {
	t.Run("flushes a pending partial line", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		w := &slogWriter{logger: logger}

		n, err := w.Write([]byte("no newline yet"))
		require.NoError(t, err)
		assert.Equal(t, 14, n)
		assert.Empty(t, buf.String(), "a line with no newline must not be logged before Close")

		require.NoError(t, w.Close())

		var record map[string]any
		require.NoError(t, json.Unmarshal(bytes.TrimRight(buf.Bytes(), "\n"), &record))
		assert.Equal(t, "no newline yet", record["msg"])
	})

	t.Run("is a no-op with nothing buffered", func(t *testing.T) {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		w := &slogWriter{logger: logger}

		require.NoError(t, w.Close())
		assert.Empty(t, buf.String(), "Close must not log anything when there was nothing buffered")
	})
}
