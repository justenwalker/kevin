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

// PackageSuite covers a user selecting a CUE package mode with the --tag flag.
//
// Tier: e2e.
type PackageSuite struct {
	e2eSuite
}

func TestPackageSuite(t *testing.T) {
	suite.Run(t, new(PackageSuite))
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

`+proxyBlock(s.T())+`
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
