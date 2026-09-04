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

// yamlEnvFile and jsonEnvFile carry the same single-step DAG as oneStepCUE,
// expressed in YAML and JSON, for the format-parity test.
const yamlEnvFile = `project: kevin-e2e-format-%s
plugins:
  echo:
    cmd: %s
proxy:
  listen: "127.0.0.1:18080"
  gateway_port: 18081
console:
  listen: "127.0.0.1:18082"
env:
  a:
    uses: echo:echo
    label: A
    with:
      message: hello from %s
`

const jsonEnvFile = `{
  "project": "kevin-e2e-format-%s",
  "plugins": {"echo": {"cmd": %s}},
  "proxy": {"listen": "127.0.0.1:18080", "gateway_port": 18081},
  "console": {"listen": "127.0.0.1:18082"},
  "env": {"a": {"uses": "echo:echo", "label": "A", "with": {"message": "hello from %s"}}}
}
`

// TestFileFormatsRunIdentically covers section 15: kevin.yaml, kevin.json,
// and a dotfile CUE variant all run the same env identically.
func (s *CLISuite) TestFileFormatsRunIdentically() {
	echoBin := strconv.Quote(s.echoPluginBin())

	cases := []struct {
		name string
		file string
		src  string
	}{
		{"yaml", "kevin.yaml", fmt.Sprintf(yamlEnvFile, "yaml", echoBin, "yaml")},
		{"json", "kevin.json", fmt.Sprintf(jsonEnvFile, "json", echoBin, "json")},
		{"dotfile-cue", ".kevin.cue", proxyBlock(s.T()) + fmt.Sprintf(oneStepCUE, "kevin-e2e-format-dotfile", echoBin, strconv.Quote("hello from dotfile"))},
	}
	for _, tc := range cases {
		s.Run(tc.name, func() {
			dir := s.T().TempDir()
			s.writeCUEFile(dir, tc.file, tc.src)

			out, code := s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "run")
			s.Equal(0, code, "output:\n%s", out)
		})
	}
}

// TestTwoFormatsInOneDirFailClearly covers the ambiguous case: kevin.cue and
// kevin.yaml both present in the same directory.
func (s *CLISuite) TestTwoFormatsInOneDirFailClearly() {
	dir := s.T().TempDir()
	echoBin := strconv.Quote(s.echoPluginBin())
	s.writeCUE(dir, fmt.Sprintf(oneStepCUE, "kevin-e2e-ambiguous", echoBin, strconv.Quote("hi")))
	s.writeCUEFile(dir, "kevin.yaml", fmt.Sprintf(yamlEnvFile, "ambiguous", echoBin, "ambiguous"))

	out, code := s.runToCompletion(dir, "-C", dir, "validate")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "multiple environment files found in")
}
