//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"
)

// CLISuite covers a user validating an environment with no Docker daemon and
// loading a dotfile-named environment file. These are cheap, independent
// one-shot commands, so each test gets its own temp project.
//
// Tier: e2e.
type CLISuite struct {
	e2eSuite
}

func TestCLISuite(t *testing.T) {
	suite.Run(t, new(CLISuite))
}

// TestValidateNeedsNoDockerDaemon covers validate against a bogus
// DOCKER_HOST: it unifies schemas and reports the step counts without ever
// touching Docker.
func (s *CLISuite) TestValidateNeedsNoDockerDaemon() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(oneStepCUE, "kevin-e2e-validate-nodocker", strconv.Quote(s.echoPluginBin()), strconv.Quote("hi"))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	p := s.startKevinWithEnv(dir, []string{"DOCKER_HOST=unix:///nonexistent/docker.sock"}, "-C", dir, "validate")
	code := s.waitExit(p, defaultTimeout)
	out := p.buf.String()
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "0 setup step(s), 1 env step(s)")
}

// TestDotfileEnvRuns covers the dotfile CUE variant (.kevin.cue) running the
// same env as a plain kevin.cue.
func (s *CLISuite) TestDotfileEnvRuns() {
	dir := s.T().TempDir()
	echoBin := strconv.Quote(s.echoPluginBin())
	s.writeCUEFile(dir, ".kevin.cue", proxyBlock(s.T())+fmt.Sprintf(oneStepCUE, "kevin-e2e-format-dotfile", echoBin, strconv.Quote("hello from dotfile")))

	out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
}
