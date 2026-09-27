package cmd_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pluginindex"
)

// testMinisignKeyText generates a fresh, unencrypted minisign key pair and
// returns its public key in minisign's 2-line text format - for
// exercising this package's own install/trust glue, not for re-testing
// go-minisign's own wire format.
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

// fixtureIndexRepo creates a git repo at a fresh temp dir with one
// plugins/demo directory at version 1.0.0, committed, and returns the
// repo's path - usable directly as an "index add" URL.
func fixtureIndexRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "plugins", "demo", "versions"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin-index.yaml"), []byte("layout: 1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugins", "demo", "plugin.yaml"),
		[]byte("name: demo\nsummary: a demo plugin\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugins", "demo", "versions", "1.0.0.yaml"),
		[]byte("version: 1.0.0\nsource:\n  oci: ghcr.io/example/demo:v1.0.0\n"), 0o600))

	run := func(args ...string) {
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	return dir
}

func TestPluginIndex(t *testing.T) {
	t.Run("add/list/remove round-trips a source", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		url := fixtureIndexRepo(t)

		addOut := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", url, "--as", "demo-repo"}))
		})
		assert.Contains(t, addOut, "demo-repo")
		assert.Contains(t, addOut, "1 plugins")

		listOut := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "list"}))
		})
		assert.Equal(t, "demo-repo\t"+url+"\n", listOut)

		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "remove", "demo-repo"}))

		listOut = captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "list"}))
		})
		assert.Empty(t, listOut)
	})

	t.Run("remove reports an unknown source", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := cmd.Run(t.Context(), []string{"plugin", "index", "remove", "nope"})
		require.ErrorIs(t, err, pluginindex.ErrUnknownSource)
	})

	t.Run("update isolates one broken source from another that succeeds", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		goodURL := fixtureIndexRepo(t)
		badURL := filepath.Join(t.TempDir(), "does-not-exist")

		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", goodURL, "--as", "good"}))
		_ = cmd.Run(t.Context(), []string{"plugin", "index", "add", badURL, "--as", "bad"}) // expected to fail; source still gets registered

		var updateOut string
		err := func() error {
			var runErr error
			updateOut = captureStdout(t, func() {
				runErr = cmd.Run(t.Context(), []string{"plugin", "index", "update"})
			})
			return runErr
		}()
		require.ErrorIs(t, err, cmd.ErrIndexUpdateFailed)
		assert.Contains(t, updateOut, "ok\tgood")
		assert.Contains(t, updateOut, "error\tbad")
	})

	t.Run("show prints metadata, version list, and the latest snippet", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		url := fixtureIndexRepo(t)
		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", url, "--as", "demo-repo"}))

		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "show", "demo"}))
		})
		assert.Contains(t, out, "name\tdemo")
		assert.Contains(t, out, "version\t1.0.0 (latest)")
		assert.Contains(t, out, `ghcr.io/example/demo:v1.0.0`)
	})

	t.Run("show --version prints only that version's snippet", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		url := fixtureIndexRepo(t)
		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", url, "--as", "demo-repo"}))

		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "show", "demo", "--version", "1.0.0"}))
		})
		assert.NotContains(t, out, "name\t")
		assert.Contains(t, out, `ghcr.io/example/demo:v1.0.0`)
	})

	t.Run("show --version reports an unknown version", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		url := fixtureIndexRepo(t)
		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", url, "--as", "demo-repo"}))

		err := cmd.Run(t.Context(), []string{"plugin", "index", "show", "demo", "--version", "9.9.9"})
		require.ErrorIs(t, err, pluginindex.ErrVersionNotFound)
	})

	t.Run("show reports an unknown plugin", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := cmd.Run(t.Context(), []string{"plugin", "index", "show", "nope"})
		require.ErrorIs(t, err, pluginindex.ErrPluginNotFound)
	})
}

// fixtureInstallRepo creates a git repo with four plugins covering
// install's trust paths: "unsigned" (no signing at all), "signed" (a
// minisign signer declared and used), "sigstore-signed" (a sigstore
// signer declared and used), and "uncovered" (a minisign-signed version
// but no matching signer declared).
func fixtureInstallRepo(t *testing.T, minisignKey string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"kevin-index.yaml": "layout: 1\n",

		"plugins/unsigned/plugin.yaml":         "name: unsigned\nsummary: no signing\n",
		"plugins/unsigned/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/unsigned:v1.0.0\n",

		"plugins/signed/plugin.yaml": "name: signed\nsummary: minisign signed\nsigners:\n" +
			"  - scheme: minisign\n    key: \"" + strings.ReplaceAll(minisignKey, "\n", `\n`) + "\"\n",
		"plugins/signed/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/signed:v1.0.0\n  signing:\n    scheme: minisign\n",

		"plugins/sigstore-signed/plugin.yaml": "name: sigstore-signed\nsummary: sigstore signed\nsigners:\n" +
			"  - scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n",
		"plugins/sigstore-signed/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/sigstore:v1.0.0\n" +
			"  signing:\n    scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com\n",

		"plugins/uncovered/plugin.yaml":         "name: uncovered\nsummary: signed version, no declared signer\n",
		"plugins/uncovered/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/uncovered:v1.0.0\n  signing:\n    scheme: minisign\n",
	}
	for path, content := range files {
		full := filepath.Join(dir, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o600))
	}

	run := func(args ...string) {
		c := exec.CommandContext(t.Context(), "git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := c.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "fixture")
	return dir
}

// writeProject creates a project directory with a minimal kevin.cue and
// returns its path.
func writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte("project: \"test\"\n"), 0o600))
	return dir
}

func TestPluginIndexInstall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	minisignKey := testMinisignKeyText(t)
	repoURL := fixtureInstallRepo(t, minisignKey)
	require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", repoURL, "--as", "demo-repo"}))

	t.Run("installs an unsigned plugin, no trust step", func(t *testing.T) {
		projectDir := writeProject(t)
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "unsigned"}))
		})
		assert.Contains(t, out, "installed\tunsigned\t1.0.0")
		assert.NotContains(t, out, "trusted")

		data, err := os.ReadFile(filepath.Join(projectDir, "kevin.cue"))
		require.NoError(t, err)
		assert.Contains(t, string(data), `ghcr.io/example/unsigned:v1.0.0`)
	})

	t.Run("refuses a second install of the same plugin", func(t *testing.T) {
		projectDir := writeProject(t)
		require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "unsigned"}))

		err := cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "unsigned"})
		require.ErrorIs(t, err, config.ErrPluginAlreadyDeclared)
	})

	t.Run("trusts a minisign signer", func(t *testing.T) {
		projectDir := writeProject(t)
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "signed"}))
		})
		assert.Contains(t, out, "trusted\t")
		assert.Contains(t, out, "installed\tsigned\t1.0.0")

		listOut := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "trust", "list"}))
		})
		assert.Contains(t, listOut, "minisign")
	})

	t.Run("trusts a sigstore signer", func(t *testing.T) {
		projectDir := writeProject(t)
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "sigstore-signed"}))
		})
		assert.Contains(t, out, "trusted\tci@example.com/https://token.actions.githubusercontent.com")

		listOut := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "trust", "list"}))
		})
		assert.Contains(t, listOut, "sigstore\tci@example.com\thttps://token.actions.githubusercontent.com")
	})

	t.Run("--no-trust skips the trust step", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", repoURL, "--as", "demo-repo"}))
		projectDir := writeProject(t)

		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "signed", "--no-trust"}))
		})
		assert.NotContains(t, out, "trusted")
		assert.Contains(t, out, "installed\tsigned\t1.0.0")

		listOut := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "trust", "list"}))
		})
		assert.Empty(t, listOut)
	})

	t.Run("warns and still installs when no signer covers the version's scheme", func(t *testing.T) {
		projectDir := writeProject(t)
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"-C", projectDir, "plugin", "index", "install", "uncovered"}))
		})
		assert.Contains(t, out, "warning\tno minisign signer declared for uncovered")
		assert.Contains(t, out, "installed\tuncovered\t1.0.0")
	})
}

func TestPluginSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	url := fixtureIndexRepo(t)
	require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "index", "add", url, "--as", "demo-repo"}))

	t.Run("no query lists every plugin", func(t *testing.T) {
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "search"}))
		})
		assert.Contains(t, out, "demo-repo/demo\ta demo plugin\t1.0.0")
	})

	t.Run("query filters by name and summary", func(t *testing.T) {
		out := captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "search", "demo"}))
		})
		assert.True(t, strings.HasPrefix(out, "demo-repo/demo\t"))

		out = captureStdout(t, func() {
			require.NoError(t, cmd.Run(t.Context(), []string{"plugin", "search", "nope"}))
		})
		assert.Empty(t, out)
	})
}
