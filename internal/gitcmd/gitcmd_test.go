package gitcmd_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/gitcmd"
)

// initFixtureRepo creates a git repo at dir with one commit, so Clone has
// something real to clone.
func initFixtureRepo(t *testing.T, dir string) {
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
	require.NoError(t, os.MkdirAll(dir, 0o755))
	run("init", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("fixture\n"), 0o600))
	run("add", "README.md")
	run("commit", "-m", "fixture")
}

func TestClone(t *testing.T) {
	t.Run("clones a local repo into a fresh directory", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src")
		initFixtureRepo(t, src)
		dst := filepath.Join(t.TempDir(), "dst")

		err := gitcmd.Clone(t.Context(), gitcmd.CloneSpec{URL: src, Dir: dst})
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(dst, "README.md"))
	})

	t.Run("fails against a nonexistent source", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		err := gitcmd.Clone(t.Context(), gitcmd.CloneSpec{URL: filepath.Join(t.TempDir(), "does-not-exist"), Dir: dst})
		require.Error(t, err)
		assert.NoDirExists(t, dst)
	})
}

func TestAvailable(t *testing.T) {
	err := gitcmd.Available(t.Context())
	require.NoError(t, err, "this repo's own CI depends on git being present")
}
