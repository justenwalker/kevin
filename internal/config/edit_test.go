package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

var demoSnippet = []byte("plugins: {\n\tdemo: {\n\t\toci: \"ghcr.io/example/demo:v1.0.0\"\n\t}\n}")

func TestInsertPlugin(t *testing.T) {
	t.Run("inserts a new plugins block", func(t *testing.T) {
		dir := write(t, `project: "demo"`)
		require.NoError(t, config.InsertPlugin(dir, "", demoSnippet))

		data, err := os.ReadFile(filepath.Join(dir, "kevin.cue"))
		require.NoError(t, err)
		assert.Contains(t, string(data), `oci: "ghcr.io/example/demo:v1.0.0"`)

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		plugins, err := f.Plugins()
		require.NoError(t, err)
		assert.Contains(t, plugins, "demo")
	})

	t.Run("merges alongside an existing plugins block", func(t *testing.T) {
		dir := write(t, "project: \"demo\"\nplugins: other: cmd: \"/bin/true\"\n")
		require.NoError(t, config.InsertPlugin(dir, "", demoSnippet))

		f, err := config.Load(dir, "", nil)
		require.NoError(t, err)
		plugins, err := f.Plugins()
		require.NoError(t, err)
		assert.Contains(t, plugins, "demo")
		assert.Contains(t, plugins, "other")
	})

	t.Run("refuses to overwrite an already-declared plugin", func(t *testing.T) {
		dir := write(t, "project: \"demo\"\nplugins: demo: cmd: \"/bin/true\"\n")
		err := config.InsertPlugin(dir, "", demoSnippet)
		require.ErrorIs(t, err, config.ErrPluginAlreadyDeclared)
	})

	t.Run("refuses a package-mode CUE environment", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte("package kevin\nproject: \"demo\"\n"), 0o600))
		err := config.InsertPlugin(dir, "", demoSnippet)
		require.ErrorIs(t, err, config.ErrUnsupportedEdit)
	})

	t.Run("preserves comments elsewhere in the file", func(t *testing.T) {
		dir := write(t, "// a helpful comment\nproject: \"demo\" // trailing note\n")
		require.NoError(t, config.InsertPlugin(dir, "", demoSnippet))

		data, err := os.ReadFile(filepath.Join(dir, "kevin.cue"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "// a helpful comment")
		assert.Contains(t, string(data), "// trailing note")
	})

	t.Run("reports a missing environment file", func(t *testing.T) {
		err := config.InsertPlugin(t.TempDir(), "", demoSnippet)
		require.ErrorIs(t, err, config.ErrNotFound)
	})
}
