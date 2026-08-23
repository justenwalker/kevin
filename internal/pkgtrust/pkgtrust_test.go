package pkgtrust_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// testKey is a freshly generated, unencrypted minisign key pair, for
// exercising pkgtrust's own glue logic - not for re-testing go-minisign's
// own wire-format correctness, which its own test suite already covers.
type testKey struct {
	sk minisign.PrivateKey
	id string // hex key id
}

func newTestKey(t *testing.T) testKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)

	sk := minisign.PrivateKey{
		SignatureAlgorithm: [2]byte{'E', 'd'},
		KeyId:              keyID,
	}
	copy(sk.SecretKey[:], priv)
	_ = pub // covered by sk.PublicKey() below

	return testKey{sk: sk, id: hex.EncodeToString(keyID[:])}
}

// pubkeyFile writes k's public key in minisign's 2-line text format to a
// file under dir and returns its path.
func (k testKey) pubkeyFile(t *testing.T, dir, name string) string {
	t.Helper()
	pub := k.sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	text := "untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

func (k testKey) sign(t *testing.T, message []byte) minisign.Signature {
	t.Helper()
	sig, err := k.sk.Sign(message, minisign.SignOptions{Hashed: true})
	require.NoError(t, err)
	return sig
}

func TestAddListRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	k := newTestKey(t)
	pubDir := t.TempDir()
	path := k.pubkeyFile(t, pubDir, "signer.pub")

	id, err := pkgtrust.Add(path)
	require.NoError(t, err)
	assert.Equal(t, k.id, id)

	// Adding the same key again is a no-op, not an error.
	id2, err := pkgtrust.Add(path)
	require.NoError(t, err)
	assert.Equal(t, id, id2)

	keys, err := pkgtrust.List()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, id, keys[0].ID)

	require.NoError(t, pkgtrust.Remove(id))
	keys, err = pkgtrust.List()
	require.NoError(t, err)
	assert.Empty(t, keys)

	err = pkgtrust.Remove(id)
	assert.ErrorIs(t, err, pkgtrust.ErrUnknownKeyID)
}

func TestLoadEmptyWhenDirMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	kr, err := pkgtrust.Load()
	require.NoError(t, err)
	assert.Empty(t, kr)
}

func TestLoadAndListDisagreeOnAMalformedFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	k := newTestKey(t)
	pubDir := t.TempDir()
	path := k.pubkeyFile(t, pubDir, "signer.pub")
	_, err := pkgtrust.Add(path)
	require.NoError(t, err)

	// A stray, non-minisign file dropped into the store by hand, alongside
	// the one real key added above.
	require.NoError(t, os.WriteFile(filepath.Join(pkgtrust.Dir(), "stray.txt"), []byte("not a key"), 0o600))

	// Load must fail on it - a Keyring can't silently drop a key an
	// operator meant to trust.
	_, err = pkgtrust.Load()
	require.ErrorIs(t, err, pkgtrust.ErrBadKey)

	// List must skip it - it's a display listing, not a trust decision.
	keys, err := pkgtrust.List()
	require.NoError(t, err)
	require.Len(t, keys, 1)
}

func TestKeyringVerify(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	k := newTestKey(t)
	other := newTestKey(t)
	pubDir := t.TempDir()
	path := k.pubkeyFile(t, pubDir, "signer.pub")
	_, err := pkgtrust.Add(path)
	require.NoError(t, err)

	kr, err := pkgtrust.Load()
	require.NoError(t, err)

	message := []byte("plugin package bytes")
	sig := k.sign(t, message)
	require.NoError(t, kr.Verify(sig, message))

	// Tampered message: signature no longer verifies.
	err = kr.Verify(sig, []byte("tampered"))
	require.ErrorIs(t, err, pkgtrust.ErrSignatureInvalid)

	// Signed by a key not in the trust store.
	otherSig := other.sign(t, message)
	err = kr.Verify(otherSig, message)
	assert.ErrorIs(t, err, pkgtrust.ErrUnknownKeyID)
}

func TestAddRejectsBadKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-key.pub")
	require.NoError(t, os.WriteFile(path, []byte("not a minisign key"), 0o600))

	_, err := pkgtrust.Add(path)
	assert.ErrorIs(t, err, pkgtrust.ErrBadKey)
}
