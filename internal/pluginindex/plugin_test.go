package pluginindex

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// testMinisignKeyText generates a fresh, unencrypted minisign key pair and
// returns its public key in minisign's 2-line text format - for
// exercising this package's own signer-loading logic, not for re-testing
// go-minisign's own wire format, which its own test suite already covers.
func testMinisignKeyText(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)
	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: keyID}
	copy(sk.SecretKey[:], priv)
	pub := sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	return "untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// writePluginDir writes a plugins/<name> directory at dir/name, with the
// given plugin.yaml body and one versions/<version>.yaml per entry in
// versions (keyed by version string, valued by the version file's source
// block, e.g. "oci: ghcr.io/example/demo:v1.0.0").
func writePluginDir(t *testing.T, dir, meta string, versions map[string]string) string {
	t.Helper()
	pluginDir := filepath.Join(dir, "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "versions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte(meta), 0o600))
	for v, source := range versions {
		body := "version: " + v + "\nsource:\n  " + source + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "versions", v+".yaml"), []byte(body), 0o600))
	}
	return pluginDir
}

func TestLoadPlugin(t *testing.T) {
	t.Run("sorts versions descending, excludes prerelease from Latest when a stable exists", func(t *testing.T) {
		dir := writePluginDir(t, t.TempDir(), "name: demo\nsummary: a demo plugin\n", map[string]string{
			"1.0.0":     "oci: ghcr.io/example/demo:v1.0.0",
			"1.2.0":     "oci: ghcr.io/example/demo:v1.2.0",
			"2.0.0-rc1": "oci: ghcr.io/example/demo:v2.0.0-rc1",
		})
		p, err := loadPlugin(dir)
		require.NoError(t, err)
		require.Len(t, p.Versions, 3)
		assert.Equal(t, []string{"2.0.0-rc1", "1.2.0", "1.0.0"}, versionStrings(p.Versions))

		latest, ok := p.Latest()
		require.True(t, ok)
		assert.Equal(t, "1.2.0", latest.Version)
	})

	t.Run("Latest falls back to the highest prerelease when it's the only version", func(t *testing.T) {
		dir := writePluginDir(t, t.TempDir(), "name: demo\nsummary: a demo plugin\n", map[string]string{
			"1.0.0-rc1": "oci: ghcr.io/example/demo:v1.0.0-rc1",
			"1.0.0-rc2": "oci: ghcr.io/example/demo:v1.0.0-rc2",
		})
		p, err := loadPlugin(dir)
		require.NoError(t, err)

		latest, ok := p.Latest()
		require.True(t, ok)
		assert.Equal(t, "1.0.0-rc2", latest.Version)
	})

	t.Run("no versions directory loads with an empty Versions, not an error", func(t *testing.T) {
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "demo")
		require.NoError(t, os.MkdirAll(pluginDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("name: demo\nsummary: a demo plugin\n"), 0o600))

		p, err := loadPlugin(pluginDir)
		require.NoError(t, err)
		assert.Empty(t, p.Versions)

		_, ok := p.Latest()
		assert.False(t, ok)
	})

	t.Run("filename/version mismatch is a load error", func(t *testing.T) {
		dir := t.TempDir()
		pluginDir := filepath.Join(dir, "demo")
		require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "versions"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "plugin.yaml"), []byte("name: demo\nsummary: a demo plugin\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "versions", "1.0.0.yaml"),
			[]byte("version: 1.0.1\nsource:\n  oci: ghcr.io/example/demo:v1.0.1\n"), 0o600))

		_, err := loadPlugin(pluginDir)
		require.ErrorIs(t, err, ErrVersionMismatch)
	})

	t.Run("missing plugin.yaml is a load error", func(t *testing.T) {
		_, err := loadPlugin(filepath.Join(t.TempDir(), "does-not-exist"))
		require.Error(t, err)
	})

	t.Run("loads a plugin's signers", func(t *testing.T) {
		key := testMinisignKeyText(t)
		meta := "name: demo\nsummary: a demo plugin\nsigners:\n" +
			"  - scheme: minisign\n    key: " + yamlQuote(key) + "\n" +
			"  - scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n"
		dir := writePluginDir(t, t.TempDir(), meta, nil)

		p, err := loadPlugin(dir)
		require.NoError(t, err)
		require.Len(t, p.Signers, 2)
		assert.Equal(t, config.SigningSchemeMinisign, p.Signers[0].Scheme)
		assert.Equal(t, key, p.Signers[0].Key)
		assert.Equal(t, config.SigningSchemeSigstore, p.Signers[1].Scheme)
		assert.Equal(t, "ci@example.com", p.Signers[1].Identity)
		assert.Equal(t, "https://token.actions.githubusercontent.com", p.Signers[1].Issuer)

		assert.Len(t, p.SignersFor(config.SigningSchemeMinisign), 1)
		assert.Len(t, p.SignersFor(config.SigningSchemeSigstore), 1)
	})

	t.Run("malformed minisign key in a signer entry is a load error", func(t *testing.T) {
		meta := "name: demo\nsummary: a demo plugin\nsigners:\n  - scheme: minisign\n    key: not-a-real-key\n"
		dir := writePluginDir(t, t.TempDir(), meta, nil)

		_, err := loadPlugin(dir)
		require.ErrorIs(t, err, ErrBadSigner)
	})
}

// yamlQuote renders s as a double-quoted YAML scalar, escaping embedded
// newlines - simpler than a block scalar for a hand-built test fixture.
func yamlQuote(s string) string {
	return `"` + strings.ReplaceAll(s, "\n", `\n`) + `"`
}

func versionStrings(versions []Version) []string {
	out := make([]string, len(versions))
	for i, v := range versions {
		out[i] = v.Version
	}
	return out
}
