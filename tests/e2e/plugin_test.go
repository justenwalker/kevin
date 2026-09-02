//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// PluginSuite covers docs/MANUAL_TESTING.md section 13 (plugin packaging),
// minus the minisign and oci parts, which need a signing key and a
// registry reachable over HTTPS. SetupSuite packs the echo plugin once,
// into a shared tarball every test reuses read-only.
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

// TestFileSourceRunsAndSkipsReExtraction covers the file: source running
// correctly, and a second run with the archive unchanged skipping
// re-extraction (the entrypoint's mtime must not change).
func (s *PluginSuite) TestFileSourceRunsAndSkipsReExtraction() {
	project := "kevin-e2e-plugin-file"
	dir := s.T().TempDir()
	s.writeCUE(dir, fmt.Sprintf(filePluginCUE, project, strconv.Quote(s.tarball), ""))
	s.cleanupProject(project)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	entrypoint := filepath.Join(dir, ".kevin", "plugins", "echo", "kevin-plugin-echo")
	info1, err := os.Stat(entrypoint)
	s.Require().NoError(err)

	// A visible clock tick, so an accidental re-extraction would produce a
	// detectably later mtime.
	time.Sleep(1100 * time.Millisecond)

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	info2, err := os.Stat(entrypoint)
	s.Require().NoError(err)
	s.Equal(info1.ModTime(), info2.ModTime(), "an unchanged archive must not be re-extracted")
}

// TestWrongChecksumFailsClosedBeforeExtraction covers checksum: - a wrong
// digest must fail before extraction, and the right one must succeed.
func (s *PluginSuite) TestWrongChecksumFailsClosedBeforeExtraction() {
	project := "kevin-e2e-plugin-checksum"
	dir := s.T().TempDir()
	wrongDigest := "checksum: \"sha256:" + hexOf("wrong") + "\""
	s.writeCUE(dir, fmt.Sprintf(filePluginCUE, project, strconv.Quote(s.tarball), wrongDigest))

	out, code := s.runToCompletion(dir, "-C", dir, "run")
	s.NotEqual(0, code, "a wrong checksum must fail before extraction, output:\n%s", out)

	data, err := os.ReadFile(s.tarball)
	s.Require().NoError(err)
	rightDigest := "checksum: \"sha256:" + hexOf(string(data)) + "\""
	s.writeCUE(dir, fmt.Sprintf(filePluginCUE, project, strconv.Quote(s.tarball), rightDigest))
	s.cleanupProject(project)

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "the right checksum must succeed, output:\n%s", out)
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
	s.writeCUE(dir, src)
	s.cleanupProject(project)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Equal(int64(1), hits.Load(), "first run must fetch once")

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Equal(int64(2), hits.Load(), "a second run with no checksum must re-fetch rather than trust a stale cache entry")
}

// TestPluginListPrintsEveryBuiltinType covers "kevin plugin list".
func (s *PluginSuite) TestPluginListPrintsEveryBuiltinType() {
	dir := s.T().TempDir()
	out, code := s.runToCompletion(dir, "plugin", "list")
	s.Equal(0, code, "output:\n%s", out)
	for _, name := range []string{
		"builtin:container", "builtin:kind",
		"builtin:kubectl", "builtin:helm", "builtin:wait", "builtin:route",
		"builtin:exec",
	} {
		s.Contains(out, name+"\n")
	}
}

func hexOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
