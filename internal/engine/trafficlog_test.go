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

	"github.com/justenwalker/kevin/internal/console"
)

func TestTrafficLog(t *testing.T) {
	t.Run("writes one JSON line per record", func(t *testing.T) {
		workspace := t.TempDir()

		tl, closer, err := openTrafficLog(workspace)
		require.NoError(t, err)

		tl.Record(t.Context(), console.Request{Method: "GET", Path: "/one"})
		tl.Record(t.Context(), console.Request{Method: "GET", Path: "/two"})
		require.NoError(t, closer.Close())

		b, err := os.ReadFile(filepath.Join(workspace, TrafficFile))
		require.NoError(t, err)

		var got []console.Request
		scanner := bufio.NewScanner(bytes.NewReader(b))
		for scanner.Scan() {
			var r console.Request
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &r))
			got = append(got, r)
		}
		require.NoError(t, scanner.Err())

		require.Len(t, got, 2)
		assert.Equal(t, "/one", got[0].Path)
		assert.Equal(t, "/two", got[1].Path)
	})

	t.Run("truncates a previous session", func(t *testing.T) {
		workspace := t.TempDir()
		path := filepath.Join(workspace, TrafficFile)
		require.NoError(t, os.WriteFile(path, []byte("stale data from a previous run\n"), 0o600))

		tl, closer, err := openTrafficLog(workspace)
		require.NoError(t, err)
		tl.Record(t.Context(), console.Request{Path: "/fresh"})
		require.NoError(t, closer.Close())

		b, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.NotContains(t, string(b), "stale data")
	})

	t.Run("a nil log no-ops instead of panicking", func(t *testing.T) {
		var tl *trafficLog
		assert.NotPanics(t, func() {
			tl.Record(t.Context(), console.Request{Path: "/x"})
		})
	})

	t.Run("a concurrent record produces well-formed lines", func(t *testing.T) {
		workspace := t.TempDir()
		tl, closer, err := openTrafficLog(workspace)
		require.NoError(t, err)

		const n = 100
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				tl.Record(t.Context(), console.Request{Path: "/x"})
				_ = i
			}(i)
		}
		wg.Wait()
		require.NoError(t, closer.Close())

		b, err := os.ReadFile(filepath.Join(workspace, TrafficFile))
		require.NoError(t, err)

		count := 0
		scanner := bufio.NewScanner(bytes.NewReader(b))
		for scanner.Scan() {
			var r console.Request
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &r), "every line must be well-formed JSON, not an interleaved write")
			count++
		}
		require.NoError(t, scanner.Err())
		assert.Equal(t, n, count)
	})
}
