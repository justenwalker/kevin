package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer is a bytes.Buffer safe for one writer goroutine and one
// reader goroutine polling String(), as a follow test needs.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func writeNDJSONLine(t *testing.T, path, step, text string) {
	t.Helper()
	line := `{"time":"2026-01-01T00:00:00Z","level":"INFO","msg":"` + text + `","step":"` + step + `","stream":"stdout"}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString(line)
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestPrintLogs(t *testing.T) {
	t.Run("prints every step's entries interleaved with no step name", func(t *testing.T) {
		logsPath := filepath.Join(t.TempDir(), "logs.ndjson")
		writeNDJSONLine(t, logsPath, "web", "starting")
		writeNDJSONLine(t, logsPath, "db", "ready")

		var buf bytes.Buffer
		require.NoError(t, printLogs(t.Context(), &buf, logsPath, t.TempDir(), "", false))
		out := buf.String()
		assert.Contains(t, out, "[stdout] starting")
		assert.Contains(t, out, "[stdout] ready")
	})

	t.Run("filters to one step", func(t *testing.T) {
		logsPath := filepath.Join(t.TempDir(), "logs.ndjson")
		writeNDJSONLine(t, logsPath, "web", "starting")
		writeNDJSONLine(t, logsPath, "db", "ready")

		var buf bytes.Buffer
		require.NoError(t, printLogs(t.Context(), &buf, logsPath, t.TempDir(), "db", false))
		assert.NotContains(t, buf.String(), "starting")
		assert.Contains(t, buf.String(), "ready")
	})

	t.Run("a step name that never logged anything prints nothing, not an error", func(t *testing.T) {
		logsPath := filepath.Join(t.TempDir(), "logs.ndjson")
		writeNDJSONLine(t, logsPath, "web", "starting")

		var buf bytes.Buffer
		require.NoError(t, printLogs(t.Context(), &buf, logsPath, t.TempDir(), "ghost", false))
		assert.Empty(t, buf.String())
	})

	t.Run("errors clearly when no run has ever logged here", func(t *testing.T) {
		var buf bytes.Buffer
		err := printLogs(t.Context(), &buf, filepath.Join(t.TempDir(), "logs.ndjson"), t.TempDir(), "", false)
		require.Error(t, err)
	})

	t.Run("without --follow, returns immediately after one read", func(t *testing.T) {
		dir := t.TempDir()
		logsPath := filepath.Join(dir, "logs.ndjson")
		writeNDJSONLine(t, logsPath, "web", "starting")

		start := time.Now()
		var buf bytes.Buffer
		require.NoError(t, printLogs(t.Context(), &buf, logsPath, t.TempDir(), "", false))
		assert.Less(t, time.Since(start), logsPollInterval, "a non-follow read must not wait for a poll interval")
		assert.Contains(t, buf.String(), "starting")
	})

	t.Run("--follow picks up a line appended after it started, then stops once the run is gone", func(t *testing.T) {
		dir := t.TempDir()
		logsPath := filepath.Join(dir, "logs.ndjson")
		writeNDJSONLine(t, logsPath, "web", "first")

		stateDir := t.TempDir()
		require.NoError(t, writePID(stateDir, os.Getpid()))

		var buf syncBuffer
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- printLogs(ctx, &buf, logsPath, stateDir, "", true) }()

		require.Eventually(t, func() bool { return strings.Contains(buf.String(), "first") }, time.Second, 10*time.Millisecond)

		writeNDJSONLine(t, logsPath, "web", "second")
		require.Eventually(t, func() bool { return strings.Contains(buf.String(), "second") }, time.Second, 10*time.Millisecond)

		// Simulate the run stopping: no more pidfile, so the next poll exits
		// the loop on its own rather than needing ctx canceled.
		removeRunState(stateDir)
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(2 * time.Second):
			t.Fatal("printLogs did not stop after the run went away")
		}
	})
}

func TestRunIsAlive(t *testing.T) {
	assert.False(t, runIsAlive(t.TempDir()), "no pidfile")

	dir := t.TempDir()
	require.NoError(t, writePID(dir, deadPID(t)))
	assert.False(t, runIsAlive(dir), "stale pidfile")

	dir = t.TempDir()
	require.NoError(t, writePID(dir, os.Getpid()))
	assert.True(t, runIsAlive(dir))
}
