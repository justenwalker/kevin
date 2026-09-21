package pkgtrust_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// writeStrayIdentityFile drops a file that doesn't parse as an identity
// record into the identity trust store, as if a user had dropped one in by
// hand.
func writeStrayIdentityFile(t *testing.T) error {
	t.Helper()
	require.NoError(t, os.MkdirAll(pkgtrust.IdentityDir(), 0o700))
	return os.WriteFile(filepath.Join(pkgtrust.IdentityDir(), "stray.json"), []byte("not json"), 0o600)
}

func TestAddListRemoveIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const identity = "ci@acme.example"
	const issuer = "https://token.actions.githubusercontent.com"

	require.NoError(t, pkgtrust.AddIdentity(identity, issuer))

	// Adding the same pair again is a no-op, not an error.
	require.NoError(t, pkgtrust.AddIdentity(identity, issuer))

	ids, err := pkgtrust.ListIdentities()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, pkgtrust.IdentityInfo{Identity: identity, Issuer: issuer}, ids[0])

	require.NoError(t, pkgtrust.VerifyIdentity(identity, issuer))

	require.NoError(t, pkgtrust.RemoveIdentity(identity, issuer))
	ids, err = pkgtrust.ListIdentities()
	require.NoError(t, err)
	assert.Empty(t, ids)

	err = pkgtrust.RemoveIdentity(identity, issuer)
	assert.ErrorIs(t, err, pkgtrust.ErrIdentityUntrusted)
}

func TestListIdentitiesEmptyWhenDirMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ids, err := pkgtrust.ListIdentities()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestVerifyIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("rejects an identity never added", func(t *testing.T) {
		err := pkgtrust.VerifyIdentity("nobody@acme.example", "https://token.actions.githubusercontent.com")
		assert.ErrorIs(t, err, pkgtrust.ErrIdentityUntrusted)
	})

	t.Run("rejects a trusted identity under the wrong issuer", func(t *testing.T) {
		require.NoError(t, pkgtrust.AddIdentity("ci@acme.example", "https://token.actions.githubusercontent.com"))
		err := pkgtrust.VerifyIdentity("ci@acme.example", "https://accounts.google.com")
		assert.ErrorIs(t, err, pkgtrust.ErrIdentityUntrusted)
	})
}

func TestListIdentitiesSkipsAStrayFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, pkgtrust.AddIdentity("ci@acme.example", "https://token.actions.githubusercontent.com"))

	require.NoError(t, writeStrayIdentityFile(t))

	ids, err := pkgtrust.ListIdentities()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, "ci@acme.example", ids[0].Identity)
}
