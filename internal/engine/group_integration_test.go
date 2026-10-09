//go:build integration

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunStepGroups covers a step group end to end: a member picks up its
// group's implicit needs without redeclaring them, the group's own outputs
// block computes from its members, and a step outside the group reads
// those outputs the same way it would read a plain step's.
func TestRunStepGroups(t *testing.T) {
	t.Run("a setup-scope group's outputs cross into env via setup.<group>", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: cluster: {
	steps: primary: {uses: "echo:echo", with: export: greeting: "from-cluster"}
	outputs: greeting: "${needs.primary.out.greeting}"
}
env: app: {uses: "echo:echo", needs: ["setup.cluster"], with: message: "${setup.cluster.out.greeting}"}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "app", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "app", "ready"))

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "from-cluster",
			"a setup-scope group's computed outputs must reach an env step across scopes")
	})

	t.Run("a setup-scope group export fails when a member can't export", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: cluster: {
	steps: primary: uses: "echo:probe"
	outputs: greeting: "${needs.primary.out.greeting}"
}
env: app: {uses: "echo:echo", needs: ["setup.cluster"], with: message: "${setup.cluster.out.greeting}"}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "app", "failed:"))
		require.Error(t, err)
		assert.Contains(t, w.String(), "does not implement export")
	})
}
