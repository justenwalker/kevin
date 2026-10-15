package pkgcache_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pkgcache"
)

func TestDirIsUnderTheUserStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KEVIN_USER_STATE_DIR", "")
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

func TestStore(t *testing.T) {
	body := "package bytes"
	sum := sha256.Sum256([]byte(body))
	want := hex.EncodeToString(sum[:])

	t.Run("caches content under its digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KEVIN_USER_STATE_DIR", "")
		entry, err := pkgcache.Store(strings.NewReader(body), "")
		require.NoError(t, err)
		assert.Equal(t, pkgcache.Path(want), entry.Path)
		assert.Equal(t, "sha256:"+want, entry.Digest)
		got, err := os.ReadFile(entry.Path)
		require.NoError(t, err)
		assert.Equal(t, body, string(got))
	})

	t.Run("accepts a matching digest", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KEVIN_USER_STATE_DIR", "")
		_, err := pkgcache.Store(strings.NewReader(body), want)
		require.NoError(t, err)
	})

	t.Run("rejects a mismatch without caching it", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv("KEVIN_USER_STATE_DIR", "")
		_, err := pkgcache.Store(strings.NewReader(body), strings.Repeat("0", 64))
		require.ErrorIs(t, err, pkgcache.ErrDigestMismatch)
		assert.NoFileExists(t, pkgcache.Path(want))
		entries, readErr := os.ReadDir(pkgcache.Sha256Dir())
		require.NoError(t, readErr)
		assert.Empty(t, entries, "the temp file must not be left behind")
	})
}
