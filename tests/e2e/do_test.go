//go:build e2e

package e2e

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// doContainerCUE brings up two container steps (a, b) and a commands: block
// exercising kevin do against builtin:container's Export - the same
// bring-up shape lifecycleCUE uses, plus commands.
const doContainerCUE = `project: "%s"

env: {
	a: {
		uses:  "builtin:container"
		label: "A"
		with: {image: "busybox:stable", cmd: ["sleep", "3600"]}
	}
	b: {
		uses:  "builtin:container"
		label: "B"
		with: {image: "busybox:stable", cmd: ["sleep", "3600"]}
	}
}

commands: {
	whoami: {
		needs: ["a"]
		run: ["sh", "-c", "echo name=${needs.a.out.name}; docker exec \"${needs.a.out.name}\" true"]
	}
	both: {
		needs: ["a", "b"]
		run: ["sh", "-c", "echo a=${needs.a.out.name} b=${needs.b.out.name}"]
	}
}
`

// DoSuite covers docs/MANUAL_TESTING.md section 9 (kevin do) against a
// plain builtin:container environment - no kind/Kubernetes required,
// unlike KindSuite's own kevin do coverage.
type DoSuite struct {
	e2eSuite
}

func TestDoSuite(t *testing.T) {
	suite.Run(t, new(DoSuite))
}

func (s *DoSuite) SetupTest() {
	s.requireDocker()
}

// TestDoExportsContainerName covers builtin:container's Export: run's
// "${needs.a.out.name}" renders to the container's real, deterministic
// name, and docker exec against it succeeds.
func (s *DoSuite) TestDoExportsContainerName() {
	project := "kevin-e2e-do-container"
	dir := s.project(project, doContainerCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("b", "ready"), defaultTimeout)

	out, code := s.runToCompletion(dir, "-C", dir, "do", "whoami")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "name=kevin-"+project+"-a")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())
	s.Empty(s.containerIDsForProject(project), "no container may remain after teardown")
}

// TestDoMergesMultipleNeeds covers a command whose needs names more than
// one step: each step's Export lands under its own name
// (${needs.a...}/${needs.b...}), unlike a flat env-var merge where two
// steps publishing the same key would collide.
func (s *DoSuite) TestDoMergesMultipleNeeds() {
	project := "kevin-e2e-do-multi-needs"
	dir := s.project(project, doContainerCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("b", "ready"), defaultTimeout)

	out, code := s.runToCompletion(dir, "-C", dir, "do", "both")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "a=kevin-"+project+"-a")
	s.Contains(out, "b=kevin-"+project+"-b")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())
}

// TestDoErrorsCleanlyWithUnknownCommand covers "kevin do <name>" naming no
// command in the commands: block - errors listing the available names,
// cleanly, not a crash.
func (s *DoSuite) TestDoErrorsCleanlyWithUnknownCommand() {
	project := "kevin-e2e-do-unknown"
	dir := s.project(project, doContainerCUE)

	out, code := s.runToCompletion(dir, "-C", dir, "do", "nope")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, `no command named "nope"`)
	s.Contains(out, "whoami")
	s.Contains(out, "both")
}
