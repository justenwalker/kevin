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

func TestFor(t *testing.T) {
	t.Setenv(UserStateDirEnv, "/user")
	t.Setenv(ProjectStateDirEnv, "")
	p := For("/work/app", "staging")

	assert.Equal(t, "/work/app/.kevin/staging", p.Workspace())
	assert.Equal(t, "/work/app/.kevin/staging/logs.ndjson", p.Logs())
	assert.Equal(t, "/user/root.crt", p.RootCert())
	assert.Equal(t, "/user/trusted-keys", p.TrustedKeys())
	assert.Equal(t, "/user/trusted-identities", p.TrustedIdentities())
	assert.Equal(t, "/user/pkg-cache", p.PackageCache())
	assert.Equal(t, "/user/plugin-index", p.PluginIndex())
	assert.Equal(t, map[string]string{
		"dir":       "/work/app",
		"root_cert": "/user/root.crt",
		"ca_cert":   "/work/app/.kevin/staging/ca.crt",
		"ca_key":    "/work/app/.kevin/staging/ca.key",
	}, p.Vars())
}

func TestForProjectEnv(t *testing.T) {
	t.Setenv(ProjectStateDirEnv, "/override")
	assert.Equal(t, "/override/logs.ndjson", For("/work/app", "x").Logs())
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
