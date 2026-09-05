package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// TestRunStepGroups covers a step group end to end: a member picks up its
// group's implicit needs without redeclaring them, the group's own outputs
// block computes from its members, and a step outside the group reads
// those outputs the same way it would read a plain step's.
func TestRunStepGroups(t *testing.T) {
	t.Run("implicit needs and computed outputs reach both a member and an outside step", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	net: {uses: "echo:echo", with: {message: "net", outputs: addr: "10.0.0.1"}}
	db: {
		needs: ["net"]
		steps: primary: {uses: "echo:echo", with: {message: "primary got ${needs.net.out.addr}", outputs: addr: "10.0.0.2"}}
		outputs: addr: "${needs.primary.out.addr}"
	}
	web: {uses: "echo:echo", needs: ["db"], with: message: "web got ${needs.db.out.addr}"}
}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "web", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "db", "ready"),
			"the group's own virtual node must reach ready alongside its members")

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		out := string(logs)
		assert.Contains(t, out, "primary got 10.0.0.1",
			"a member must see its group's implicit needs without redeclaring them")
		assert.Contains(t, out, "web got 10.0.0.2",
			"a step outside the group must read the group's computed outputs the same way it reads a plain step's")
	})

	t.Run("a member's needs can reference a sibling member by its bare name", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: db: {
	steps: {
		primary: {uses: "echo:echo", with: {message: "primary", outputs: addr: "10.0.0.2"}}
		replica: {uses: "echo:echo", needs: ["primary"], with: message: "replica got ${needs.primary.out.addr}"}
	}
}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "db.replica", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "db.primary", "ready"),
			"a sibling reference must still order primary before replica")

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "replica got 10.0.0.2",
			"a member's with block must resolve a sibling's bare name to its outputs")
	})

	t.Run("tears down cleanly", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: db: steps: primary: uses: "echo:echo"
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "db.primary", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "db.primary", "removed"))
	})

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

	t.Run("a step outside a group cannot address a member directly", func(t *testing.T) {
		dir := project(t, `
env: {
	db: steps: primary: uses: "echo:echo"
	web: {uses: "echo:echo", needs: ["db.primary"]}
}
`)
		err := runEnv(t, dir)
		require.Error(t, err)
		assert.ErrorIs(t, err, config.ErrUnaddressableMember)
	})
}
