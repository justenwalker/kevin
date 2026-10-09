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
	args: run: ["echo", "base"]
}
`

// DoSuite covers a user running kevin do against a
// plain builtin:container environment - no kind/Kubernetes required,
// unlike KindSuite's own kevin do coverage.
//
// Tier: e2e.
type DoSuite struct {
	e2eSuite
}

func TestDoSuite(t *testing.T) {
	suite.Run(t, new(DoSuite))
}

func (s *DoSuite) SetupTest() {
	s.requireDocker()
}

// TestDoRunsCommandsAgainstALiveEnvironment covers kevin do against one live
// environment: builtin:container's Export rendered into run, a command
// needing two steps (each lands under its own name), and extra args after
// -- appended to the command's own run argv.
func (s *DoSuite) TestDoRunsCommandsAgainstALiveEnvironment() {
	project := "kevin-e2e-do-container"
	dir := s.project(project, doContainerCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("b", "ready"), defaultTimeout)

	out, code := s.runToCompletion(dir, "-C", dir, "do", "whoami")
	s.Require().Equal(0, code, "output:\n%s", out)
	s.Contains(out, "name=kevin-"+project+"-a")

	out, code = s.runToCompletion(dir, "-C", dir, "do", "both")
	s.Require().Equal(0, code, "output:\n%s", out)
	s.Contains(out, "a=kevin-"+project+"-a")
	s.Contains(out, "b=kevin-"+project+"-b")

	out, code = s.runToCompletion(dir, "-C", dir, "do", "args", "--", "extra", "words")
	s.Require().Equal(0, code, "output:\n%s", out)
	s.Contains(out, "base extra words")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())
	s.Empty(s.containerIDsForProject(project), "no container may remain after teardown")
}
