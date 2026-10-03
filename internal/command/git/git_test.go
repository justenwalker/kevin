package git_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/commandtest"
	"github.com/justenwalker/kevin/internal/command/git"
	"github.com/justenwalker/kevin/internal/gittest"
	"github.com/justenwalker/kevin/internal/uerr"
)

func TestMain(m *testing.M) {
	gittest.Isolate()
	os.Exit(m.Run())
}

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

		err := git.New(command.Default).Clone(t.Context(), git.CloneSpec{URL: src, Dir: dst})
		require.NoError(t, err)
		assert.FileExists(t, filepath.Join(dst, "README.md"))
	})

	t.Run("fails against a nonexistent source", func(t *testing.T) {
		dst := filepath.Join(t.TempDir(), "dst")
		err := git.New(command.Default).Clone(t.Context(), git.CloneSpec{URL: filepath.Join(t.TempDir(), "does-not-exist"), Dir: dst})
		require.Error(t, err)
		assert.NoDirExists(t, dst)
	})
}

func TestAvailable(t *testing.T) {
	err := git.New(command.Default).Available(t.Context())
	require.NoError(t, err, "this repo's own CI depends on git being present")
}

func TestClientClone(t *testing.T) {
	t.Run("clones url into dir with the extra environment", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Equal(t, []string{"git", "clone", "https://example.com/r.git", "/dst"}, cmd.Args)
				assert.Contains(t, cmd.Env, "GIT_TERMINAL_PROMPT=0")
				return nil
			})

		err := git.New(runner).Clone(t.Context(), git.CloneSpec{
			URL: "https://example.com/r.git", Dir: "/dst", Env: map[string]string{"GIT_TERMINAL_PROMPT": "0"},
		})
		require.NoError(t, err)
	})

	t.Run("leaves the environment alone without extra variables", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				assert.Nil(t, cmd.Env)
				return nil
			})

		require.NoError(t, git.New(runner).Clone(t.Context(), git.CloneSpec{URL: "u", Dir: "/dst"}))
	})

	t.Run("includes git's stderr in the error", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, cmd *exec.Cmd) error {
				_, _ = io.WriteString(cmd.Stderr, "repository not found\n")
				return errors.New("exit status 128")
			})

		err := git.New(runner).Clone(t.Context(), git.CloneSpec{URL: "u", Dir: "/dst"})
		require.ErrorContains(t, err, "repository not found")
	})

	t.Run("a missing binary reads as not installed", func(t *testing.T) {
		runner := commandtest.NewMockRunner(t)
		runner.EXPECT().Run(mock.Anything, mock.Anything).Return(exec.ErrNotFound)

		err := git.New(runner).Clone(t.Context(), git.CloneSpec{URL: "u", Dir: "/dst"})
		require.ErrorIs(t, err, exec.ErrNotFound)
		assert.Contains(t, uerr.Display(err), "git isn't installed")
	})
}
