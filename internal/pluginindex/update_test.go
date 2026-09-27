package pluginindex_test

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

	"github.com/justenwalker/kevin/internal/pluginindex"
)

// testFederationKey is a freshly generated, unencrypted minisign key
// pair, for exercising a federated VersionSource's signature requirement
// end to end - not for re-testing go-minisign's own wire format, which
// internal/pkgtrust's own tests already cover.
type testFederationKey struct {
	sk minisign.PrivateKey
}

func newTestFederationKey(t *testing.T) testFederationKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)
	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: keyID}
	copy(sk.SecretKey[:], priv)
	return testFederationKey{sk: sk}
}

func (k testFederationKey) text() string {
	pub := k.sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	return "untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// signFile signs path's contents and writes the detached signature to
// path+".minisig".
func (k testFederationKey) signFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := k.sk.Sign(data, minisign.SignOptions{Hashed: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".minisig", sig.Encode(), 0o600))
}

func TestUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	goodURL := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	badURL := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := pluginindex.AddSource(t.Context(), goodURL, "good")
	require.NoError(t, err)

	// AddSource's initial clone fails, but the source stays registered
	// for Update to retry.
	_, err = pluginindex.AddSource(t.Context(), badURL, "bad")
	require.Error(t, err)

	results, err := pluginindex.Update(t.Context())
	require.NoError(t, err, "Update's own error is reserved for a failure outside any one source")
	require.Len(t, results, 2)

	byAlias := map[string]pluginindex.UpdateResult{}
	for _, r := range results {
		byAlias[r.Source.Alias] = r
	}

	good := byAlias["good"]
	require.NoError(t, good.Err)
	assert.Equal(t, 1, good.Plugins)

	bad := byAlias["bad"]
	require.Error(t, bad.Err, "one source's clone failure is isolated to its own UpdateResult")
}

func TestUpdateWarnsOnUncoveredSigner(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	url := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml": pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0",
			"oci: ghcr.io/example/demo:v1.0.0\n  signing:\n    scheme: minisign"),
	})
	_, err := pluginindex.AddSource(t.Context(), url, "demo-repo")
	require.NoError(t, err)

	results, err := pluginindex.Update(t.Context())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	require.Len(t, results[0].Warnings, 1)
	assert.Contains(t, results[0].Warnings[0], "no matching signer")
}

func TestUpdatePicksUpNewVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	url := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	_, err := pluginindex.AddSource(t.Context(), url, "demo-repo")
	require.NoError(t, err)

	catalog, err := pluginindex.LoadAll(t.Context())
	require.NoError(t, err)
	demo, err := catalog.Resolve("demo")
	require.NoError(t, err)
	latest, ok := demo.Latest()
	require.True(t, ok)
	assert.Equal(t, "1.0.0", latest.Version)

	// Simulate a release CI job: pure file-add, no edits to any existing
	// file - the append-only property the format is designed around.
	writeFixtureFiles(t, url, map[string]string{
		"plugins/demo/versions/1.5.0.yaml": pluginVersion("1.5.0", "oci: ghcr.io/example/demo:v1.5.0"),
	})
	commitFixtureRepo(t, url, "demo 1.5.0")

	results, err := pluginindex.Update(t.Context())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.NoError(t, results[0].Err)
	assert.Equal(t, 1, results[0].Plugins)

	catalog, err = pluginindex.LoadAll(t.Context())
	require.NoError(t, err)
	demo, err = catalog.Resolve("demo")
	require.NoError(t, err)
	require.Len(t, demo.Versions, 2)
	latest, ok = demo.Latest()
	require.True(t, ok)
	assert.Equal(t, "1.5.0", latest.Version)
}

func TestUpdateFederatedVersionSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key := newTestFederationKey(t)

	versionRepo := fixtureRepo(t, map[string]string{
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	versionFile := filepath.Join(versionRepo, "plugins", "demo", "versions", "1.0.0.yaml")
	key.signFile(t, versionFile)
	commitFixtureRepo(t, versionRepo, "sign 1.0.0")

	indexRepo := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml": pluginMeta("demo", "a federated demo plugin") +
			"signers:\n  - scheme: minisign\n    key: \"" + strings.ReplaceAll(key.text(), "\n", `\n`) + "\"\n" +
			"version_source: " + versionRepo + "\n",
	})

	res, err := pluginindex.AddSource(t.Context(), indexRepo, "fed-repo")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Plugins)
	assert.Empty(t, res.Warnings)

	catalog, err := pluginindex.LoadAll(t.Context())
	require.NoError(t, err)
	demo, err := catalog.Resolve("demo")
	require.NoError(t, err)
	require.Len(t, demo.Versions, 1)
	assert.Equal(t, "1.0.0", demo.Versions[0].Version)
}

func TestUpdateFederatedVersionSourceUnreachable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	indexRepo := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml": pluginMeta("demo", "a federated demo plugin") +
			"version_source: " + filepath.Join(t.TempDir(), "does-not-exist") + "\n",
	})

	res, err := pluginindex.AddSource(t.Context(), indexRepo, "fed-repo")
	require.NoError(t, err)
	assert.Equal(t, 0, res.Plugins, "no signers declared, so the plugin fails to load rather than silently trusting nothing")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "version_source is set but no signers are declared")
}

func TestUpdateFederatedVersionSourceRequiresMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key := newTestFederationKey(t)

	versionRepo := t.TempDir()
	writeFixtureFiles(t, versionRepo, map[string]string{
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	commitFixtureRepo(t, versionRepo, "fixture, no kevin-index.yaml")
	versionFile := filepath.Join(versionRepo, "plugins", "demo", "versions", "1.0.0.yaml")
	key.signFile(t, versionFile)
	commitFixtureRepo(t, versionRepo, "sign 1.0.0")

	indexRepo := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml": pluginMeta("demo", "a federated demo plugin") +
			"signers:\n  - scheme: minisign\n    key: \"" + strings.ReplaceAll(key.text(), "\n", `\n`) + "\"\n" +
			"version_source: " + versionRepo + "\n",
	})

	res, err := pluginindex.AddSource(t.Context(), indexRepo, "fed-repo")
	require.NoError(t, err)
	assert.Equal(t, 0, res.Plugins, "the version source has no kevin-index.yaml, so the plugin fails to load")
	require.Len(t, res.Warnings, 1)
	assert.Contains(t, res.Warnings[0], "not a kevin plugin index")
}
