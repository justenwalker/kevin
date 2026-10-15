package index_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/plugins/index"
	"github.com/justenwalker/kevin/internal/plugins/signing"
)

func TestInstall(t *testing.T) {
	const identity, issuer = "ci@example.com", "https://token.actions.githubusercontent.com"
	t.Setenv("HOME", t.TempDir())

	url := fixtureRepo(t, map[string]string{
		"plugins/unsigned/plugin.yaml":         pluginMeta("unsigned", "no signing"),
		"plugins/unsigned/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/unsigned:v1.0.0"),

		"plugins/signed/plugin.yaml": pluginMeta("signed", "sigstore signed") +
			"signers:\n  - scheme: sigstore\n    identity: " + identity + "\n    issuer: " + issuer + "\n",
		"plugins/signed/versions/1.0.0.yaml": pluginVersion("1.0.0",
			"oci: ghcr.io/example/signed:v1.0.0\n  signing:\n    scheme: sigstore\n    identity: "+identity+"\n    issuer: "+issuer),

		"plugins/uncovered/plugin.yaml": pluginMeta("uncovered", "signed version, no declared signer"),
		"plugins/uncovered/versions/1.0.0.yaml": pluginVersion("1.0.0",
			"oci: ghcr.io/example/uncovered:v1.0.0\n  signing:\n    scheme: minisign"),
	})
	_, err := index.AddSource(t.Context(), url, "demo-repo")
	require.NoError(t, err)
	catalog, err := index.LoadAll(t.Context())
	require.NoError(t, err)

	request := func(t *testing.T, name string, noTrust bool) index.InstallRequest {
		t.Helper()
		p, err := catalog.Resolve(name)
		require.NoError(t, err)
		v, ok := p.Latest()
		require.True(t, ok)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte("project: \"test\"\n"), 0o600))
		return index.InstallRequest{Plugin: p, Version: v, Dir: dir, NoTrust: noTrust}
	}

	t.Run("an unsigned version trusts nothing and adds its entry", func(t *testing.T) {
		req := request(t, "unsigned", false)

		result, err := index.Install(req)

		require.NoError(t, err)
		assert.Empty(t, result.Trusted)
		assert.Empty(t, result.Warnings)
		data, err := os.ReadFile(filepath.Join(req.Dir, "kevin.cue"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "ghcr.io/example/unsigned:v1.0.0")
	})

	t.Run("a signed version trusts the declared signer", func(t *testing.T) {
		result, err := index.Install(request(t, "signed", false))

		require.NoError(t, err)
		assert.Equal(t, []string{identity + "/" + issuer}, result.Trusted)
		ids, err := signing.ListIdentities()
		require.NoError(t, err)
		require.Len(t, ids, 1)
		assert.Equal(t, identity, ids[0].Identity)
	})

	t.Run("NoTrust adds the entry without trusting the signer", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())

		result, err := index.Install(request(t, "signed", true))

		require.NoError(t, err)
		assert.Empty(t, result.Trusted)
		ids, err := signing.ListIdentities()
		require.NoError(t, err)
		assert.Empty(t, ids)
	})

	t.Run("a signed version with no matching signer warns and still installs", func(t *testing.T) {
		req := request(t, "uncovered", false)

		result, err := index.Install(req)

		require.NoError(t, err)
		assert.Empty(t, result.Trusted)
		assert.Equal(t, []string{"no minisign signer declared for uncovered"}, result.Warnings)
		data, err := os.ReadFile(filepath.Join(req.Dir, "kevin.cue"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "ghcr.io/example/uncovered:v1.0.0")
	})

	t.Run("a second install reports what it trusted and ErrPluginAlreadyDeclared", func(t *testing.T) {
		req := request(t, "signed", false)
		_, err := index.Install(req)
		require.NoError(t, err)

		result, err := index.Install(req)

		require.ErrorIs(t, err, config.ErrPluginAlreadyDeclared)
		assert.Equal(t, []string{identity + "/" + issuer}, result.Trusted)
	})
}
