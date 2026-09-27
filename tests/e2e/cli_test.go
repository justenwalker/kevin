//go:build e2e

package e2e

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/suite"
)

// CLISuite covers docs/MANUAL_TESTING.md sections 10 (validate/init), 14
// (reserved plugin namespace), and 15 (environment file formats). These are
// cheap, independent one-shot commands, so each test gets its own temp
// project.
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

// TestValidateFailsOnBrokenSchemaBeforeDocker covers a with block that
// fails schema-unify (image given as a number, not a string): validate must
// fail with a clear CUE error, before anything Docker-related runs.
func (s *CLISuite) TestValidateFailsOnBrokenSchemaBeforeDocker() {
	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+`project: "kevin-e2e-validate-broken"

env: web: {
	uses: "builtin:container"
	with: image: 123
}
`)

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "image")
}

// TestValidateFailsOnMissingListenPorts covers proxy.listen, proxy.gateway_port,
// and console.listen all being required with no schema default: omitting the
// proxy:/console: block entirely fails validate clearly, naming the field,
// before anything Docker-related runs.
func (s *CLISuite) TestValidateFailsOnMissingListenPorts() {
	dir := s.T().TempDir()
	s.writeCUE(dir, `project: "kevin-e2e-validate-missing-ports"`)

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "proxy.listen")
}

// TestInitPrintsPluginNameForCmdSourceAndStartsNoProcess covers init: it
// lists every non-builtin plugin a step uses, cmd:-sourced or not, and
// starts no process (a cmd: plugin needs nothing downloaded).
func (s *CLISuite) TestInitPrintsPluginNameForCmdSourceAndStartsNoProcess() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(oneStepCUE, "kevin-e2e-init", strconv.Quote(s.echoPluginBin()), strconv.Quote("hi"))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	out, code := s.runToCompletion(dir, "-C", dir, "init")
	s.Equal(0, code, "output:\n%s", out)
	s.Equal("echo\n", out, "init must print exactly the plugin name, one per line")
}

// TestReservedPluginNamespaceFailsValidation covers section 14: a plugins:
// key from the reserved list is rejected, naming every reserved name.
func (s *CLISuite) TestReservedPluginNamespaceFailsValidation() {
	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+`project: "kevin-e2e-reserved"

plugins: kevin: {cmd: "./anything"}

env: a: {
	uses: "kevin:whatever"
}
`)

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "reserved name")
	s.Contains(out, "builtin")
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

// TestTwoCandidatesInOneDirFailClearly covers the ambiguous case: kevin.cue
// and its dotfile variant both present in the same directory.
func (s *CLISuite) TestTwoCandidatesInOneDirFailClearly() {
	dir := s.T().TempDir()
	echoBin := strconv.Quote(s.echoPluginBin())
	s.writeCUE(dir, fmt.Sprintf(oneStepCUE, "kevin-e2e-ambiguous", echoBin, strconv.Quote("hi")))
	s.writeCUEFile(dir, ".kevin.cue", fmt.Sprintf(oneStepCUE, "kevin-e2e-ambiguous", echoBin, strconv.Quote("hi")))

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "multiple environment files found in")
}
