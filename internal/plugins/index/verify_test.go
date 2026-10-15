package index

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/commandtest"
	"github.com/justenwalker/kevin/internal/command/cosign"
	"github.com/justenwalker/kevin/internal/config"
)

// testSignerKey is a freshly generated, unencrypted minisign key pair,
// for exercising this package's own verification glue - not for
// re-testing go-minisign's own wire-format correctness, which
// internal/plugins/signing's own tests already cover.
type testSignerKey struct {
	sk minisign.PrivateKey
}

func newTestSignerKey(t *testing.T) testSignerKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)
	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: keyID}
	copy(sk.SecretKey[:], priv)
	return testSignerKey{sk: sk}
}

// text renders the key's public half in minisign's 2-line text format.
func (k testSignerKey) text() string {
	pub := k.sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	return "untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// signer wraps the key's public half as a minisign [Signer].
func (k testSignerKey) signer() Signer {
	return Signer{Scheme: config.SigningSchemeMinisign, Key: k.text()}
}

// signFile signs path's contents and writes the detached signature to
// path+".minisig".
func (k testSignerKey) signFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := k.sk.Sign(data, minisign.SignOptions{Hashed: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".minisig", sig.Encode(), 0o600))
}

func TestVerifyVersionFile(t *testing.T) {
	t.Run("a valid minisig sibling verifies", func(t *testing.T) {
		key := newTestSignerKey(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "1.0.0.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\n"), 0o600))
		key.signFile(t, path)

		err := verifyVersionFile(t.Context(), path, []Signer{key.signer()}, command.Default)
		require.NoError(t, err)
	})

	t.Run("no sibling signature is ErrVersionSignatureMissing", func(t *testing.T) {
		key := newTestSignerKey(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "1.0.0.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\n"), 0o600))

		err := verifyVersionFile(t.Context(), path, []Signer{key.signer()}, command.Default)
		require.ErrorIs(t, err, ErrVersionSignatureMissing)
	})

	t.Run("a signature from an untrusted key is ErrVersionSignatureInvalid", func(t *testing.T) {
		signingKey := newTestSignerKey(t)
		declaredKey := newTestSignerKey(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "1.0.0.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\n"), 0o600))
		signingKey.signFile(t, path)

		err := verifyVersionFile(t.Context(), path, []Signer{declaredKey.signer()}, command.Default)
		require.ErrorIs(t, err, ErrVersionSignatureInvalid)
	})

	t.Run("tampered content is ErrVersionSignatureInvalid", func(t *testing.T) {
		key := newTestSignerKey(t)
		dir := t.TempDir()
		path := filepath.Join(dir, "1.0.0.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\n"), 0o600))
		key.signFile(t, path)

		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\nsource:\n  oci: evil\n"), 0o600))

		err := verifyVersionFile(t.Context(), path, []Signer{key.signer()}, command.Default)
		require.ErrorIs(t, err, ErrVersionSignatureInvalid)
	})

	t.Run("a sigstore.json sibling is verified through the runner", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "1.0.0.yaml")
		require.NoError(t, os.WriteFile(path, []byte("version: 1.0.0\n"), 0o600))
		require.NoError(t, os.WriteFile(path+".sigstore.json", []byte("{}"), 0o600))
		signers := []Signer{{Scheme: config.SigningSchemeSigstore, Identity: "ci@example.com", Issuer: "https://issuer.example"}}

		t.Run("accepts a signature the runner accepts", func(t *testing.T) {
			runner := commandtest.NewMockRunner(t)
			runner.EXPECT().Run(mock.Anything, mock.Anything).RunAndReturn(
				func(_ context.Context, cmd *exec.Cmd) error {
					assert.Contains(t, cmd.Args, "ci@example.com")
					assert.Contains(t, cmd.Args, path)
					return nil
				})

			require.NoError(t, verifyVersionFile(t.Context(), path, signers, runner))
		})

		t.Run("a signature the runner rejects is ErrVersionSignatureInvalid", func(t *testing.T) {
			runner := commandtest.NewMockRunner(t)
			runner.EXPECT().Run(mock.Anything, mock.Anything).Return(assert.AnError)

			err := verifyVersionFile(t.Context(), path, signers, runner)

			require.ErrorIs(t, err, ErrVersionSignatureInvalid)
		})

		t.Run("a missing cosign is ErrVersionSignatureInvalid, not skipped", func(t *testing.T) {
			runner := commandtest.NewMockRunner(t)
			runner.EXPECT().Run(mock.Anything, mock.Anything).Return(exec.ErrNotFound)

			err := verifyVersionFile(t.Context(), path, signers, runner)

			require.ErrorIs(t, err, ErrVersionSignatureInvalid)
			require.ErrorIs(t, err, cosign.ErrCosignNotFound)
		})
	})
}

func TestMinisignKeyring(t *testing.T) {
	t.Run("ignores non-minisign signers", func(t *testing.T) {
		signers := []Signer{{Scheme: config.SigningSchemeSigstore, Identity: "a", Issuer: "b"}}
		assert.Empty(t, minisignKeyring(signers))
	})

	t.Run("includes every minisign signer, keyed by key id", func(t *testing.T) {
		key := newTestSignerKey(t)
		kr := minisignKeyring([]Signer{key.signer()})
		require.Len(t, kr, 1)
		_, ok := kr[key.sk.PublicKey().KeyId]
		assert.True(t, ok)
	})
}
