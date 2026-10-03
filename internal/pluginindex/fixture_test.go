package pluginindex_test

import (
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/gittest"
)

func TestMain(m *testing.M) {
	gittest.Isolate()
	os.Exit(m.Run())
}

// indexMarker is a valid kevin-index.yaml body (layout 1).
const indexMarker = "layout: 1\n"

// fixtureRepo creates a git repo at a fresh temp dir containing files (path
// relative to the repo root, mapped to its content), commits them, and
// returns the repo's path - usable directly as a [pluginindex.Source] URL.
// A valid kevin-index.yaml is included by default; pass one explicitly in
// files to override it (e.g. to test a missing or malformed marker).
func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"kevin-index.yaml": indexMarker}
	maps.Copy(all, files)
	writeFixtureFiles(t, dir, all)
	commitFixtureRepo(t, dir, "fixture")
	return dir
}

// writeFixtureFiles writes files (path relative to dir, mapped to
// content) under dir, without committing.
func writeFixtureFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}
}

// commitFixtureRepo runs git init (if needed) and commits every file
// currently in dir with message.
func commitFixtureRepo(t *testing.T, dir, message string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		run("init", "-q", "-b", "main")
	}
	run("add", "-A")
	run("commit", "-q", "-m", message)
}

// pluginMeta builds a plugins/<name>/plugin.yaml body.
func pluginMeta(name, summary string) string {
	return "name: " + name + "\nsummary: " + summary + "\n"
}

// pluginVersion builds a plugins/<name>/versions/<version>.yaml body for a
// single-line source block (e.g. `oci: ghcr.io/example/demo:v1.0.0`).
func pluginVersion(version, source string) string {
	return "version: " + version + "\nsource:\n  " + source + "\n"
}
