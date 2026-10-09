//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"
)

// ExecSuite covers a user running host commands with builtin:exec: its up and
// down commands and stdout chaining through the real engine. No Docker, exec
// runs on the host directly.
//
// Tier: e2e.
type ExecSuite struct {
	e2eSuite
}

func TestExecSuite(t *testing.T) {
	suite.Run(t, new(ExecSuite))
}

// TestExecChainsStdoutAndRunsDownOnTeardown proves an exec step's up output
// reaches a dependent through ${needs.<step>.out.stdout}, and that its
// down.command runs once teardown starts.
func (s *ExecSuite) TestExecChainsStdoutAndRunsDownOnTeardown() {
	project := "kevin-e2e-exec"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	src := fmt.Sprintf(`project: %s

env: {
	a: {
		uses:  "builtin:exec"
		label: "A"
		with: {
			up:   command: ["sh", "-c", "echo hello-from-exec"]
			down: command: ["sh", "-c", "echo down-ran"]
		}
	}
	b: {
		uses:  "builtin:exec"
		label: "B"
		needs: ["a"]
		with: up: command: ["sh", "-c", "echo got: ${needs.a.out.stdout}"]
	}
}
`, strconv.Quote(project))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	out, code := s.runUntil(dir, stepLine("b", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	logs := s.readLogs(dir)
	s.Contains(logs, "got: hello-from-exec",
		"a dependent step must read the exec step's trimmed stdout as its \"stdout\" output")
	s.Contains(logs, "down-ran", "down.command must run once teardown starts")
}

// TestExecWithoutDownRunsNothingAtTeardown proves a step with no down block
// runs no command when the environment is torn down.
func (s *ExecSuite) TestExecWithoutDownRunsNothingAtTeardown() {
	project := "kevin-e2e-exec-nodown"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	runs := filepath.Join(dir, "runs")
	src := fmt.Sprintf(`project: %s

env: a: {
	uses: "builtin:exec"
	with: up: command: ["sh", "-c", "echo up >> %s"]
}
`, strconv.Quote(project), runs)
	s.writeCUE(dir, proxyBlock(s.T())+src)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	data, err := os.ReadFile(runs)
	s.Require().NoError(err)
	s.Equal("up\n", string(data), "only the up command may have run")
}

// TestExecSplicesTheProxyAddress proves ${project.http_proxy_addr} is the
// address kevin prints for its own proxy.
func (s *ExecSuite) TestExecSplicesTheProxyAddress() {
	project := "kevin-e2e-exec-proxyaddr"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	src := fmt.Sprintf(`project: %s

env: a: {
	uses: "builtin:exec"
	with: up: command: ["sh", "-c", "echo addr=${project.http_proxy_addr}"]
}
`, strconv.Quote(project))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	m := regexp.MustCompile(`proxy\s+http://(\S+)\s`).FindStringSubmatch(out)
	s.Require().NotNil(m, "kevin must print its proxy address, output:\n%s", out)
	s.Contains(s.readLogs(dir), "echo addr="+m[1]+`"`)
}
