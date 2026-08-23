package pkgcache_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/internal/pkgcache"
)

func TestDirIsUnderTheHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, ".kevin", "pkg-cache"), pkgcache.Dir())
}

func TestPathIsUnderDirBySha256(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, ".kevin", "pkg-cache", "sha256", "deadbeef"), pkgcache.Path("deadbeef"))
}

func TestSha256DirIsThePathsParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Dir(pkgcache.Path("deadbeef")), pkgcache.Sha256Dir())
}
