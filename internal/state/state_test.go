package state

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserStateDir(t *testing.T) {
	t.Run("env set", func(t *testing.T) {
		t.Setenv(UserStateDirEnv, "/custom/user/dir")
		assert.Equal(t, "/custom/user/dir", UserStateDir())
	})

	t.Run("env unset defaults to home/.kevin", func(t *testing.T) {
		t.Setenv(UserStateDirEnv, "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		assert.Equal(t, filepath.Join(home, ".kevin"), UserStateDir())
	})
}

func TestProjectStateDir(t *testing.T) {
	cwd := "/some/project"

	t.Run("env set", func(t *testing.T) {
		t.Setenv(ProjectStateDirEnv, "/custom/project/dir")
		assert.Equal(t, "/custom/project/dir", ProjectStateDir(cwd, ""))
	})

	t.Run("env unset defaults to cwd/.kevin", func(t *testing.T) {
		t.Setenv(ProjectStateDirEnv, "")
		assert.Equal(t, filepath.Join(cwd, ".kevin"), ProjectStateDir(cwd, ""))
	})

	t.Run("env unset with name defaults to cwd/.kevin/name", func(t *testing.T) {
		t.Setenv(ProjectStateDirEnv, "")
		assert.Equal(t, filepath.Join(cwd, ".kevin", "staging"), ProjectStateDir(cwd, "staging"))
	})
}

func TestMustHomeDir(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		assert.Equal(t, home, mustHomeDir())
	})

	t.Run("panics when home dir unresolvable", func(t *testing.T) {
		t.Setenv("HOME", "")
		assert.Panics(t, func() { mustHomeDir() })
	})
}
