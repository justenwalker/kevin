//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"
)

// PackageSuite covers docs/MANUAL_TESTING.md section 21: CUE package-mode
// directory loading and the --tag flag.
type PackageSuite struct {
	e2eSuite
}

func TestPackageSuite(t *testing.T) {
	suite.Run(t, new(PackageSuite))
}

// TestPackageModeSplitsAcrossFiles covers a package split into kevin.cue and
// a second file sharing its package clause: validate must report both
// files' fields as one environment, with no import statement between them.
func (s *PackageSuite) TestPackageModeSplitsAcrossFiles() {
	dir := s.T().TempDir()
	echoBin := strconv.Quote(s.echoPluginBin())

	s.writeCUE(dir, fmt.Sprintf(`package kevin

project: "kevin-e2e-package-split"
plugins: echo: cmd: %s
env: a: {uses: "echo:echo", with: message: "hi"}
`, echoBin))
	s.writeCUEFile(dir, "mirrors.cue", `package kevin

env: b: {uses: "echo:echo", with: message: "hi from mirrors"}
`)

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "kevin-e2e-package-split: 0 setup step(s), 2 env step(s)",
		"both files' env steps must merge into one environment")
}

// TestTagFlipsMode covers --tag: a bare -t airgap flips an @tag-gated field,
// bridged into a schema-defaulted field via the "if airgap {...}" pattern
// docs/guides/proxy-and-egress.md documents, and spliced into a step's
// message via CUE's own string interpolation so the durable log proves it
// landed.
func (s *PackageSuite) TestTagFlipsMode() {
	dir := s.T().TempDir()
	project := "kevin-e2e-tag-flip"
	echoBin := strconv.Quote(s.echoPluginBin())
	s.cleanupProject(project)

	s.writeCUE(dir, fmt.Sprintf(`package kevin

project: %s
airgap: bool | *false @tag(airgap,type=bool)
note: *"normal" | string
if airgap {
	note: "airgap-mode"
}
plugins: echo: cmd: %s
env: a: {uses: "echo:echo", with: message: "note is \(note)"}
`, strconv.Quote(project), echoBin))

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	logs, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	s.Contains(string(logs), "note is normal", "airgap defaults false, so note must keep its own default")

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "-t", "airgap", "run")
	s.Equal(0, code, "output:\n%s", out)
	logs, err = os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	s.Contains(string(logs), "note is airgap-mode", "a bare -t airgap must behave like -t airgap=true")
}

// TestPackageConflictFailsClearly covers the legacy-format-plus-package-mode
// conflict: a kevin.yaml alongside a .cue sibling that declares a package
// fails clearly, naming the conflicting file.
func (s *PackageSuite) TestPackageConflictFailsClearly() {
	dir := s.T().TempDir()
	echoBin := strconv.Quote(s.echoPluginBin())

	s.writeCUEFile(dir, "kevin.yaml", fmt.Sprintf(yamlEnvFile, "package-conflict", echoBin, "package-conflict"))
	s.writeCUEFile(dir, "mirrors.cue", `package kevin

domain: "should-not-load"
`)

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "mirrors.cue", "the error must name the conflicting file")
}
