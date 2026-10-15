package signing_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/plugins/signing"
)

// writeStrayIdentityFile drops a file that doesn't parse as an identity
// record into the identity trust store, as if a user had dropped one in by
// hand.
func writeStrayIdentityFile(t *testing.T) error {
	t.Helper()
	require.NoError(t, os.MkdirAll(signing.IdentityDir(), 0o700))
	return os.WriteFile(filepath.Join(signing.IdentityDir(), "stray.json"), []byte("not json"), 0o600)
}

func TestAddListRemoveIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const identity = "ci@acme.example"
	const issuer = "https://token.actions.githubusercontent.com"

	require.NoError(t, signing.AddIdentity(identity, issuer))

	// Adding the same pair again is a no-op, not an error.
	require.NoError(t, signing.AddIdentity(identity, issuer))

	ids, err := signing.ListIdentities()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, signing.IdentityInfo{Identity: identity, Issuer: issuer}, ids[0])

	require.NoError(t, signing.VerifyIdentity(identity, issuer))

	require.NoError(t, signing.RemoveIdentity(identity, issuer))
	ids, err = signing.ListIdentities()
	require.NoError(t, err)
	assert.Empty(t, ids)

	err = signing.RemoveIdentity(identity, issuer)
	assert.ErrorIs(t, err, signing.ErrIdentityUntrusted)
}

func TestListIdentitiesEmptyWhenDirMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ids, err := signing.ListIdentities()
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestVerifyIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("rejects an identity never added", func(t *testing.T) {
		err := signing.VerifyIdentity("nobody@acme.example", "https://token.actions.githubusercontent.com")
		assert.ErrorIs(t, err, signing.ErrIdentityUntrusted)
	})

	t.Run("rejects a trusted identity under the wrong issuer", func(t *testing.T) {
		require.NoError(t, signing.AddIdentity("ci@acme.example", "https://token.actions.githubusercontent.com"))
		err := signing.VerifyIdentity("ci@acme.example", "https://accounts.google.com")
		assert.ErrorIs(t, err, signing.ErrIdentityUntrusted)
	})
}

func TestListIdentitiesSkipsAStrayFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	require.NoError(t, signing.AddIdentity("ci@acme.example", "https://token.actions.githubusercontent.com"))

	require.NoError(t, writeStrayIdentityFile(t))

	ids, err := signing.ListIdentities()
	require.NoError(t, err)
	require.Len(t, ids, 1)
	assert.Equal(t, "ci@acme.example", ids[0].Identity)
}
