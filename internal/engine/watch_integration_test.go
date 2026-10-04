//go:build integration

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartWatch(t *testing.T) {
	t.Run("reruns a watched step and its dependent", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	a: {uses: "echo:echo", watch: ["src"], with: message: "A"}
	p: {uses: "echo:probe", needs: ["a"]}
}
`)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o750))

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		w := &watcher{}
		done := runAsync(t, ctx, dir, w)
		waitForCount(t, w, "p                ready", 1, 30*time.Second)

		// The watcher starts just after bring-up, so keep editing until a
		// change registers.
		for i := 0; !strings.Contains(w.String(), ", rerunning"); i++ {
			require.Less(t, i, 20, "no rerun after an edit:\n%s", w.String())
			require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte(strconv.Itoa(i)), 0o600))
			time.Sleep(500 * time.Millisecond) // longer than the debounce
		}
		waitForCount(t, w, "change in "+filepath.Join("src", "a.go")+", rerunning", 1, 10*time.Second)
		waitForCount(t, w, "a                ready", 2, 10*time.Second)
		waitForCount(t, w, "p                ready", 2, 10*time.Second)

		cancel()
		<-done
	})

	t.Run("a change during a rerun causes one more rerun", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: a: {uses: "echo:echo", watch: ["src"], with: {message: "A", delay: "1s"}}
`)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "src"), 0o750))

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		w := &watcher{}
		done := runAsync(t, ctx, dir, w)
		waitForCount(t, w, "a                ready", 1, 30*time.Second)
		// The watcher starts once Up returns, after the step's delay.
		time.Sleep(1500 * time.Millisecond)

		file := filepath.Join(dir, "src", "a.go")
		require.NoError(t, os.WriteFile(file, []byte("0"), 0o600))
		waitForCount(t, w, ", rerunning", 1, 10*time.Second)
		for _, c := range "123456789" {
			require.NoError(t, os.WriteFile(file, []byte(string(c)), 0o600))
			time.Sleep(20 * time.Millisecond)
		}
		waitForCount(t, w, "a                ready", 3, 15*time.Second)
		time.Sleep(2 * time.Second)
		assert.Equal(t, 2, strings.Count(w.String(), ", rerunning"))

		cancel()
		<-done
	})
}
