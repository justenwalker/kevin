package watch_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/watch"
)

const debounce = 50 * time.Millisecond

func newWatcher(t *testing.T, dir string, steps map[string][]string) *watch.Watcher {
	t.Helper()
	w, err := watch.New(dir, steps, debounce)
	require.NoError(t, err)
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func expectEvent(t *testing.T, w *watch.Watcher) watch.Event {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case <-time.After(5 * time.Second):
		require.FailNow(t, "no event")
		return watch.Event{}
	}
}

func expectQuiet(t *testing.T, w *watch.Watcher) {
	t.Helper()
	select {
	case ev := <-w.Events():
		assert.Failf(t, "unexpected event", "%+v", ev)
	case <-time.After(10 * debounce):
	}
}

func TestWatcher(t *testing.T) {
	t.Run("a write triggers once", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o750))
		w := newWatcher(t, dir, map[string][]string{"api": {"src"}})

		writeFile(t, filepath.Join(dir, "src", "a.go"), "x")
		ev := expectEvent(t, w)
		assert.Equal(t, "api", ev.Step)
		assert.Equal(t, filepath.Join("src", "a.go"), ev.Path)
		expectQuiet(t, w)
	})

	t.Run("a burst triggers once", func(t *testing.T) {
		dir := t.TempDir()
		w := newWatcher(t, dir, map[string][]string{"api": {"."}})

		for i := range 10 {
			writeFile(t, filepath.Join(dir, "a.txt"), string(rune('a'+i)))
		}
		expectEvent(t, w)
		expectQuiet(t, w)
	})

	t.Run("ignored paths never trigger", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".kevin"), 0o750))
		require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o750))
		w := newWatcher(t, dir, map[string][]string{"api": {"."}})

		writeFile(t, filepath.Join(dir, ".kevin", "state"), "x")
		writeFile(t, filepath.Join(dir, ".git", "HEAD"), "x")
		writeFile(t, filepath.Join(dir, "a.go~"), "x")
		writeFile(t, filepath.Join(dir, ".#a.go"), "x")
		writeFile(t, filepath.Join(dir, "a.go.swp"), "x")
		expectQuiet(t, w)
	})

	t.Run("a new subdirectory is watched", func(t *testing.T) {
		dir := t.TempDir()
		w := newWatcher(t, dir, map[string][]string{"api": {"."}})

		sub := filepath.Join(dir, "sub")
		require.NoError(t, os.Mkdir(sub, 0o750))
		expectEvent(t, w)

		writeFile(t, filepath.Join(sub, "b.go"), "x")
		ev := expectEvent(t, w)
		assert.Equal(t, filepath.Join("sub", "b.go"), ev.Path)
	})

	t.Run("a watched file triggers only its step", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "a.txt"), "x")
		writeFile(t, filepath.Join(dir, "b.txt"), "x")
		w := newWatcher(t, dir, map[string][]string{"a": {"a.txt"}, "b": {"b.txt"}})

		writeFile(t, filepath.Join(dir, "b.txt"), "y")
		assert.Equal(t, "b", expectEvent(t, w).Step)
		expectQuiet(t, w)
	})

	t.Run("a rename over a watched file triggers", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "a.txt")
		writeFile(t, target, "x")
		w := newWatcher(t, dir, map[string][]string{"a": {"a.txt"}})

		tmp := filepath.Join(dir, "a.txt.tmp")
		writeFile(t, tmp, "y")
		require.NoError(t, os.Rename(tmp, target))
		assert.Equal(t, "a", expectEvent(t, w).Step)
	})

	t.Run("close with an event pending is safe", func(t *testing.T) {
		dir := t.TempDir()
		w, err := watch.New(dir, map[string][]string{"a": {"."}}, debounce)
		require.NoError(t, err)

		writeFile(t, filepath.Join(dir, "a.txt"), "x")
		time.Sleep(debounce / 2)
		require.NoError(t, w.Close())
		time.Sleep(2 * debounce)
	})

	t.Run("a missing path fails", func(t *testing.T) {
		_, err := watch.New(t.TempDir(), map[string][]string{"api": {"nope"}}, debounce)
		require.Error(t, err)
	})
}
