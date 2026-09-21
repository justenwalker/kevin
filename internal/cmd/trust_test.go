package cmd_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/pkgtrust"
)

func TestPluginTrust(t *testing.T) {
	t.Run("add/list/remove round-trips a key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())

		pubDir := t.TempDir()
		pubPath := pubDir + "/signer.pub"
		// A real minisign public key (42 decoded bytes: "Ed" + 8-byte key id
		// + 32-byte Ed25519 key) - content doesn't need to correspond to a
		// real secret key for add/list/remove, only for Verify.
		require.NoError(t, os.WriteFile(pubPath,
			[]byte("untrusted comment: test key\nRWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3\n"),
			0o600))

		var runErr error
		addOut := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "add", pubPath})
		})
		require.NoError(t, runErr)
		id := strings.TrimSpace(addOut)
		assert.NotEmpty(t, id)

		listOut := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Contains(t, listOut, id)

		runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "remove", id})
		require.NoError(t, runErr)

		listOut = captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Empty(t, strings.TrimSpace(listOut))
	})

	t.Run("add rejects a bad key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		badPath := t.TempDir() + "/not-a-key.pub"
		require.NoError(t, os.WriteFile(badPath, []byte("not a minisign key"), 0o600))

		err := cmd.Run(t.Context(), []string{"plugin", "trust", "add", badPath})
		require.ErrorIs(t, err, pkgtrust.ErrBadKey)
	})

	t.Run("remove reports an unknown key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := cmd.Run(t.Context(), []string{"plugin", "trust", "remove", "deadbeefdeadbeef"})
		require.ErrorIs(t, err, pkgtrust.ErrUnknownKeyID)
	})

	t.Run("add-identity/list/remove-identity round-trips an identity", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		const identity = "ci@acme.example"
		const issuer = "https://token.actions.githubusercontent.com"

		runErr := cmd.Run(t.Context(), []string{"plugin", "trust", "add-identity", "--identity", identity, "--issuer", issuer})
		require.NoError(t, runErr)

		listOut := captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Contains(t, listOut, "sigstore\t"+identity+"\t"+issuer)

		runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "remove-identity", identity, issuer})
		require.NoError(t, runErr)

		listOut = captureStdout(t, func() {
			runErr = cmd.Run(t.Context(), []string{"plugin", "trust", "list"})
		})
		require.NoError(t, runErr)
		assert.Empty(t, strings.TrimSpace(listOut))
	})

	t.Run("add-identity requires both flags", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		var cmdErr *cmd.CommandError
		err := cmd.Run(t.Context(), []string{"plugin", "trust", "add-identity", "--identity", "ci@acme.example"})
		require.ErrorAs(t, err, &cmdErr, "a missing required flag is a usage error")
	})

	t.Run("remove-identity reports an untrusted identity", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := cmd.Run(t.Context(), []string{"plugin", "trust", "remove-identity", "nobody@acme.example", "https://accounts.google.com"})
		require.ErrorIs(t, err, pkgtrust.ErrIdentityUntrusted)
	})
}
