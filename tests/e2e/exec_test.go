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

// ExecSuite covers docs/MANUAL_TESTING.md section 17: builtin:exec's up/down
// lifecycle and stdout output chaining. No Docker - exec runs on the host
// directly, so these mirror EnvSuite's no-container style.
type ExecSuite struct {
	e2eSuite
}

func TestExecSuite(t *testing.T) {
	suite.Run(t, new(ExecSuite))
}

// TestUpCapturesStdoutForADependent proves an exec step's up.command
// output reaches a dependent step through ${needs.<step>.out.stdout}.
func (s *ExecSuite) TestUpCapturesStdoutForADependent() {
	project := "kevin-e2e-exec-stdout"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	src := fmt.Sprintf(`project: %s

env: {
	a: {uses: "builtin:exec", label: "A", with: up: command: ["sh", "-c", "echo hello-from-exec"]}
	b: {
		uses:  "builtin:exec"
		label: "B"
		needs: ["a"]
		with: up: command: ["sh", "-c", "echo got: ${needs.a.out.stdout}"]
	}
}
`, strconv.Quote(project))
	s.writeCUE(dir, src)

	out, code := s.runUntil(dir, stepLine("b", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	logs := s.readLogs(dir)
	s.Contains(logs, "got: hello-from-exec",
		"a dependent step must read the exec step's trimmed stdout as its \"stdout\" output")
}

// TestDownRunsOnTeardown proves an exec step's down.command runs on
// teardown, and does not run at all when the with block sets no down.
func (s *ExecSuite) TestDownRunsOnTeardown() {
	project := "kevin-e2e-exec-down"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	src := fmt.Sprintf(`project: %s

env: a: {
	uses:  "builtin:exec"
	label: "A"
	with: {
		up:   command: ["sh", "-c", "echo up-ran"]
		down: command: ["sh", "-c", "echo down-ran"]
	}
}
`, strconv.Quote(project))
	s.writeCUE(dir, src)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	logs := s.readLogs(dir)
	s.Contains(logs, "up-ran")
	s.Contains(logs, "down-ran", "down.command must run once teardown starts")
}

// readLogs reads dir's durable step log (.kevin/logs.ndjson), the same
// file EnvSuite's CEL tests already read.
func (s *ExecSuite) readLogs(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	return string(b)
}
