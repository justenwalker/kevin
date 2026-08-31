package engine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenNDJSONLog(t *testing.T) {
	t.Run("writes one JSON line per entry", func(t *testing.T) {
		workspace := t.TempDir()

		logger, err := openNDJSONLog(workspace, LogsFile)
		require.NoError(t, err)

		logger.Info("hello", "step", "web", "stream", "stdout")
		logger.Info("world", "step", "api", "stream", "stderr")
		require.NoError(t, logger.Close())

		b, err := os.ReadFile(filepath.Join(workspace, LogsFile))
		require.NoError(t, err)

		var lines []map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(b))
		for scanner.Scan() {
			var m map[string]any
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &m))
			lines = append(lines, m)
		}
		require.NoError(t, scanner.Err())

		require.Len(t, lines, 2)
		assert.Equal(t, "hello", lines[0]["msg"])
		assert.Equal(t, "web", lines[0]["step"])
		assert.Equal(t, "stdout", lines[0]["stream"])
		assert.Equal(t, "world", lines[1]["msg"])
	})

	t.Run("truncates a previous session", func(t *testing.T) {
		workspace := t.TempDir()
		path := filepath.Join(workspace, LogsFile)
		require.NoError(t, os.WriteFile(path, []byte("stale data from a previous run\n"), 0o600))

		logger, err := openNDJSONLog(workspace, LogsFile)
		require.NoError(t, err)
		logger.Info("fresh")
		require.NoError(t, logger.Close())

		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "stale data")
	})

	t.Run("a concurrent write produces well-formed lines", func(t *testing.T) {
		workspace := t.TempDir()
		logger, err := openNDJSONLog(workspace, TrafficFile)
		require.NoError(t, err)

		const n = 100
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				logger.Info("request", "path", "/x")
			})
		}
		wg.Wait()
		require.NoError(t, logger.Close())

		b, err := os.ReadFile(filepath.Join(workspace, TrafficFile))
		require.NoError(t, err)

		count := 0
		scanner := bufio.NewScanner(bytes.NewReader(b))
		for scanner.Scan() {
			var m map[string]any
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &m), "every line must be well-formed JSON, not an interleaved write")
			count++
		}
		require.NoError(t, scanner.Err())
		assert.Equal(t, n, count)
	})
}
