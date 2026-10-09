//go:build e2e

package e2e

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// PluginIndexSuite covers docs/site/content/docs/guides/plugin-discovery.md:
// kevin plugin index add/list/remove/update/show, kevin plugin search, and
// kevin plugin index install, against a real local git fixture repo. The
// federated version_source + sigstore case needs cosign and an interactive
// OIDC login, so it is checked by hand; this suite covers minisign only.
//
// Tier: e2e.
type PluginIndexSuite struct {
	e2eSuite
}

func TestPluginIndexSuite(t *testing.T) {
	suite.Run(t, new(PluginIndexSuite))
}

// isolatedHome is a "HOME=<fresh temp dir>" env entry for
// runToCompletionWithEnv, so a test's ~/.kevin/plugin-index cache and
// trust store never touch the real machine's own.
func (s *PluginIndexSuite) isolatedHome() []string {
	return []string{"HOME=" + s.T().TempDir()}
}

// gitFixture creates a local git repo at a fresh temp dir containing
// files (path relative to the repo root, mapped to content), commits
// them, and returns the repo's path - usable directly as an "index add"
// URL.
func (s *PluginIndexSuite) gitFixture(files map[string]string) string {
	t := s.T()
	t.Helper()
	dir := t.TempDir()
	for path, content := range files {
		full := filepath.Join(dir, path)
		s.Require().NoError(os.MkdirAll(filepath.Dir(full), 0o755))
		s.Require().NoError(os.WriteFile(full, []byte(content), 0o600))
	}
	s.gitCommit(dir, "fixture")
	return dir
}

// gitCommit runs git init (if needed) and commits every file currently in
// dir with message - for a fixture repo's initial commit, or a follow-up
// one simulating a release CI job adding a new file.
func (s *PluginIndexSuite) gitCommit(dir, message string) {
	t := s.T()
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@example.com",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@example.com",
		)
		out, err := cmd.CombinedOutput()
		s.Require().NoErrorf(err, "git %v: %s", args, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		run("init", "-q", "-b", "main")
	}
	run("add", "-A")
	run("commit", "-q", "-m", message)
}

// e2eSignerKey is a freshly generated, unencrypted minisign key pair, for
// exercising "kevin plugin index install"'s trust step against a real
// signature - not for re-testing go-minisign's own wire format.
type e2eSignerKey struct {
	sk minisign.PrivateKey
}

func newE2ESignerKey(t *testing.T) e2eSignerKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)
	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: keyID}
	copy(sk.SecretKey[:], priv)
	return e2eSignerKey{sk: sk}
}

// pubText renders the key's public half in minisign's 2-line text format.
func (k e2eSignerKey) pubText() string {
	pub := k.sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	return "untrusted comment: e2e test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// yamlText is pubText escaped for embedding in a double-quoted YAML scalar.
func (k e2eSignerKey) yamlText() string {
	return strings.ReplaceAll(k.pubText(), "\n", `\n`)
}

// signFile writes path's detached signature next to it as path+".minisig".
func (k e2eSignerKey) signFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := k.sk.Sign(data, minisign.SignOptions{Hashed: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".minisig", sig.Encode(), 0o600))
}

// TestAddListShowSearchRemove covers the read-only discovery commands
// end to end against a real cloned fixture repo.
func (s *PluginIndexSuite) TestAddListShowSearchRemove() {
	env := s.isolatedHome()
	url := s.gitFixture(map[string]string{
		"kevin-index.yaml":                 "layout: 1\n",
		"plugins/demo/plugin.yaml":         "name: demo\nsummary: a demo plugin\n",
		"plugins/demo/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/demo:v1.0.0\n",
	})
	dir := s.T().TempDir()

	out, code := s.runToCompletionWithEnv(dir, env, "plugin", "index", "add", url, "--as", "e2e-repo")
	s.Require().Equal(0, code, "output:\n%s", out)
	s.Contains(out, "e2e-repo")
	s.Contains(out, "1 plugins")

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "index", "list")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "e2e-repo\t"+url)

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "index", "show", "demo")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "name\tdemo")
	s.Contains(out, "version\t1.0.0 (latest)")
	s.Contains(out, `ghcr.io/example/demo:v1.0.0`)

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "search", "demo")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "e2e-repo/demo\ta demo plugin\t1.0.0")

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "index", "remove", "e2e-repo")
	s.Equal(0, code, "output:\n%s", out)

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "index", "list")
	s.Equal(0, code, "output:\n%s", out)
	s.NotContains(out, "e2e-repo")
}

// TestInstallTrustsSignerAndWritesSnippet covers "kevin plugin index
// install" end to end: it resolves a signed plugin, trusts its declared
// minisign signer, writes the plugins: entry into a real kevin.cue, and
// the result still validates.
func (s *PluginIndexSuite) TestInstallTrustsSignerAndWritesSnippet() {
	env := s.isolatedHome()
	key := newE2ESignerKey(s.T())
	url := s.gitFixture(map[string]string{
		"kevin-index.yaml": "layout: 1\n",
		"plugins/signed/plugin.yaml": "name: signed\nsummary: a signed demo plugin\nsigners:\n" +
			"  - scheme: minisign\n    key: \"" + key.yamlText() + "\"\n",
		"plugins/signed/versions/1.0.0.yaml": "version: 1.0.0\nsource:\n  oci: ghcr.io/example/signed:v1.0.0\n  signing:\n    scheme: minisign\n",
	})

	project := "kevin-e2e-plugin-index-install"
	projectDir := s.project(project, "project: \"%s\"\n")

	out, code := s.runToCompletionWithEnv(projectDir, env, "plugin", "index", "add", url, "--as", "e2e-repo")
	s.Require().Equal(0, code, "output:\n%s", out)

	out, code = s.runToCompletionWithEnv(projectDir, env, "-C", projectDir, "plugin", "index", "install", "signed")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "trusted\t")
	s.Contains(out, "installed\tsigned\t1.0.0")

	data, err := os.ReadFile(filepath.Join(projectDir, "kevin.cue"))
	s.Require().NoError(err)
	s.Contains(string(data), `ghcr.io/example/signed:v1.0.0`)

	out, code = s.runToCompletionWithEnv(projectDir, env, "-C", projectDir, "plugin", "trust", "list")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "minisign")

	out, code = s.runToCompletionWithEnv(projectDir, env, "-C", projectDir, "validate")
	s.Equal(0, code, "output:\n%s", out)

	// A second install of the same plugin must refuse, not overwrite.
	out, code = s.runToCompletionWithEnv(projectDir, env, "-C", projectDir, "plugin", "index", "install", "signed")
	s.NotEqual(0, code, "output:\n%s", out)
}
