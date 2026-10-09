//go:build e2e

package e2e

import (
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"github.com/stretchr/testify/suite"
)

// PluginSuite covers a user packaging, signing, and fetching plugins,
// minus the sigstore part, which needs an interactive OIDC login. The oci
// part runs against an in-process TLS registry, trusted through
// KEVIN_PLUGIN_CA_FILE. SetupSuite packs the echo plugin once,
// into a shared tarball every test reuses read-only.
//
// Tier: e2e.
type PluginSuite struct {
	e2eSuite

	tarball string
}

func TestPluginSuite(t *testing.T) {
	suite.Run(t, new(PluginSuite))
}

func (s *PluginSuite) SetupSuite() {
	t := s.T()
	require := s.Require()

	srcDir := t.TempDir()
	echoBin, err := os.ReadFile(s.echoPluginBin())
	require.NoError(err)
	entrypoint := filepath.Join(srcDir, "kevin-plugin-echo")
	require.NoError(os.WriteFile(entrypoint, echoBin, 0o755))

	s.tarball = filepath.Join(t.TempDir(), "echo.tar.gz")
	out, code := s.runToCompletion(srcDir, "plugin", "pack", srcDir,
		"-o", s.tarball, "--name", "echo", "--version", "1.0.0", "--entrypoint", "kevin-plugin-echo")
	require.Equal(0, code, "output:\n%s", out)
	require.Contains(out, "echo 1.0.0 -> "+s.tarball)

	_, err = os.Stat(filepath.Join(srcDir, "manifest.json"))
	require.True(os.IsNotExist(err), "manifest.json must be written only into the archive, not the source dir")
}

// filePluginCUE runs one echo step through a plugins: echo: file: source.
const filePluginCUE = `project: "%s"

plugins: echo: {
	file: %s
%s
}

env: a: {
	uses:  "echo:echo"
	label: "A"
	with: message: "hi"
}
`

// TestFileSourceRuns covers the file: source running correctly and extracting
// into the project workspace.
func (s *PluginSuite) TestFileSourceRuns() {
	project := "kevin-e2e-plugin-file"
	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+fmt.Sprintf(filePluginCUE, project, strconv.Quote(s.tarball), ""))
	s.cleanupProject(project)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	_, err := os.Stat(filepath.Join(dir, ".kevin", "plugins", "echo", "kevin-plugin-echo"))
	s.Require().NoError(err, "the package must be extracted into the project workspace")
}

// TestHTTPSourceRefetchesEveryRunWithNoChecksum covers an http: source
// served by httptest.NewServer (stdlib, no external process): it works the
// same way as file:, and with no checksum it re-fetches every run rather
// than trusting a stale cache entry.
func (s *PluginSuite) TestHTTPSourceRefetchesEveryRunWithNoChecksum() {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.ServeFile(w, r, s.tarball)
	}))
	defer srv.Close()

	project := "kevin-e2e-plugin-http"
	dir := s.T().TempDir()
	src := `project: "` + project + `"

plugins: echo: http: "` + srv.URL + `/echo.tar.gz"

env: a: {
	uses:  "echo:echo"
	label: "A"
	with: message: "hi"
}
`
	s.writeCUE(dir, proxyBlock(s.T())+src)
	s.cleanupProject(project)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Equal(int64(1), hits.Load(), "first run must fetch once")

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Equal(int64(2), hits.Load(), "a second run with no checksum must re-fetch rather than trust a stale cache entry")
}

// TestOCISourceTrustsThePluginCAFile covers push to and an oci: source from a
// registry with a private CA: both fail without KEVIN_PLUGIN_CA_FILE, both
// work with it, and a second project resolves the same package without
// downloading its blob again.
func (s *PluginSuite) TestOCISourceTrustsThePluginCAFile() {
	var blobGets atomic.Int64
	registry := ociserver.New(ocimem.New(), nil)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/blobs/") {
			blobGets.Add(1)
		}
		registry.ServeHTTP(w, r)
	}))
	defer srv.Close()

	caPath := filepath.Join(s.T().TempDir(), "registry-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	s.Require().NoError(os.WriteFile(caPath, caPEM, 0o600))

	home := s.T().TempDir()
	baseEnv := []string{"KEVIN_PLUGIN_CA_FILE=", "HOME=" + home, "KEVIN_USER_STATE_DIR=" + filepath.Join(home, ".kevin")}
	trustedEnv := append(slices.Clone(baseEnv), "KEVIN_PLUGIN_CA_FILE="+caPath)

	host := strings.TrimPrefix(srv.URL, "https://")
	ref := host + "/echo:v1"
	workDir := s.T().TempDir()

	out, code := s.runToCompletionWithEnv(workDir, baseEnv, "plugin", "push", s.tarball, ref)
	s.NotEqual(0, code, "push must fail without the CA, output:\n%s", out)
	s.Contains(out, "certificate")

	out, code = s.runToCompletionWithEnv(workDir, trustedEnv, "plugin", "push", s.tarball, ref)
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "sha256:")

	runOCI := func(project string, env []string) *kevinProc {
		dir := s.T().TempDir()
		src := `project: "` + project + `"

plugins: echo: oci: "` + ref + `"

env: a: {
	uses:  "echo:echo"
	label: "A"
	with: message: "hi"
}
`
		s.writeCUE(dir, proxyBlock(s.T())+src)
		s.cleanupProject(project)
		return s.startKevinWithEnv(dir, env, "-C", dir, "run")
	}

	failing := runOCI("kevin-e2e-plugin-oci-untrusted", baseEnv)
	s.NotEqual(0, s.waitExit(failing, defaultTimeout), "run must fail without the CA, output:\n%s", failing.buf.String())
	s.Contains(failing.buf.String(), "certificate")

	first := runOCI("kevin-e2e-plugin-oci", trustedEnv)
	s.waitFor(first, stepLine("a", "ready"), defaultTimeout)
	first.stop()
	fetched := blobGets.Load()
	s.Positive(fetched, "the first run must download the package blob")

	second := runOCI("kevin-e2e-plugin-oci-again", trustedEnv)
	s.waitFor(second, stepLine("a", "ready"), defaultTimeout)
	second.stop()
	s.Equal(fetched, blobGets.Load(), "a second project must reuse the cached blob")
}

// TestPushPublishesTheSignatureSibling covers pushing a signed package: with
// a .minisig next to the tarball, push prints the package digest and then
// the signature's own.
func (s *PluginSuite) TestPushPublishesTheSignatureSibling() {
	srv := httptest.NewTLSServer(ociserver.New(ocimem.New(), nil))
	defer srv.Close()

	caPath := filepath.Join(s.T().TempDir(), "registry-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	s.Require().NoError(os.WriteFile(caPath, caPEM, 0o600))

	home := s.T().TempDir()
	env := []string{"KEVIN_PLUGIN_CA_FILE=" + caPath, "HOME=" + home, "KEVIN_USER_STATE_DIR=" + filepath.Join(home, ".kevin")}

	pkg := filepath.Join(s.T().TempDir(), "echo.tar.gz")
	data, err := os.ReadFile(s.tarball)
	s.Require().NoError(err)
	s.Require().NoError(os.WriteFile(pkg, data, 0o600))
	newE2ESignerKey(s.T()).signFile(s.T(), pkg)

	ref := strings.TrimPrefix(srv.URL, "https://") + "/echo:v1"
	out, code := s.runToCompletionWithEnv(s.T().TempDir(), env, "plugin", "push", pkg, ref)
	s.Equal(0, code, "output:\n%s", out)
	s.Regexp(`(?m)^\S+@sha256:[0-9a-f]{64}$`, out, "the package digest")
	s.Regexp(`(?m)^\S+@sha256:[0-9a-f]{64} \(signature\)$`, out, "the signature digest")
}

// TestMinisignSigningFailsClosed covers signing: scheme: "minisign" on a
// file: source: trust add/list/remove, then an untrusted key, a trusted
// key, a tampered package and a missing .minisig.
func (s *PluginSuite) TestMinisignSigningFailsClosed() {
	require := s.Require()
	home := s.T().TempDir()
	env := []string{"HOME=" + home, "KEVIN_USER_STATE_DIR=" + filepath.Join(home, ".kevin")}

	pkgDir := s.T().TempDir()
	pkg := filepath.Join(pkgDir, "echo.tar.gz")
	data, err := os.ReadFile(s.tarball)
	require.NoError(err)
	require.NoError(os.WriteFile(pkg, data, 0o600))

	key := newE2ESignerKey(s.T())
	key.signFile(s.T(), pkg)
	pubPath := filepath.Join(pkgDir, "echo.pub")
	require.NoError(os.WriteFile(pubPath, []byte(key.pubText()), 0o600))

	dir := s.T().TempDir()
	project := "kevin-e2e-plugin-minisign"
	s.writeCUE(dir, proxyBlock(s.T())+fmt.Sprintf(filePluginCUE, project, strconv.Quote(pkg),
		`signing: scheme: "minisign"`))
	s.cleanupProject(project)

	out, code := s.runToCompletionWithEnv(dir, env, "-C", dir, "run")
	s.NotEqual(0, code, "an untrusted key must fail closed, output:\n%s", out)
	s.Contains(out, "isn't trusted")

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "trust", "add", pubPath)
	require.Equal(0, code, "output:\n%s", out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	keyID := lines[len(lines)-1]
	require.Regexp(`^[0-9a-f]{16}$`, keyID)

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "trust", "list")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, keyID)

	p := s.startKevinWithEnv(dir, env, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "ready"), defaultTimeout)
	p.stop()

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "trust", "remove", keyID)
	require.Equal(0, code, "output:\n%s", out)
	out, code = s.runToCompletionWithEnv(dir, env, "-C", dir, "run")
	s.NotEqual(0, code, "a removed key must fail closed, output:\n%s", out)
	s.Contains(out, "isn't trusted")

	out, code = s.runToCompletionWithEnv(dir, env, "plugin", "trust", "add", pubPath)
	require.Equal(0, code, "output:\n%s", out)

	require.NoError(os.WriteFile(pkg, append(slices.Clone(data), 0), 0o600))
	out, code = s.runToCompletionWithEnv(dir, env, "-C", dir, "run")
	s.NotEqual(0, code, "a tampered package must fail closed, output:\n%s", out)
	s.Contains(out, "doesn't verify against its package")

	require.NoError(os.WriteFile(pkg, data, 0o600))
	require.NoError(os.Remove(pkg + ".minisig"))
	out, code = s.runToCompletionWithEnv(dir, env, "-C", dir, "run")
	s.NotEqual(0, code, "a missing .minisig must fail closed, output:\n%s", out)
	s.Contains(out, "ships no signature file")
}
