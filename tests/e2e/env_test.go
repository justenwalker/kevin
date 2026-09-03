//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// oneStepCUE is a minimal single-step echo DAG - no Docker resource, so the
// named-environment and CEL tests stay fast and need no container.
const oneStepCUE = `project: "%s"

plugins: echo: cmd: %s

env: a: {
	uses:  "echo:echo"
	label: "A"
	with: message: %s
}
`

// EnvSuite covers docs/MANUAL_TESTING.md sections 11 (named environments)
// and 12 (cross-step values / CEL expressions).
type EnvSuite struct {
	e2eSuite
}

func TestEnvSuite(t *testing.T) {
	suite.Run(t, new(EnvSuite))
}

// writeOneStep renders oneStepCUE with a literal (already-quoted) message
// expression and writes it to dir/kevin.cue.
func (s *EnvSuite) writeOneStep(dir, project, message string) {
	src := fmt.Sprintf(oneStepCUE, project, strconv.Quote(s.echoPluginBin()), strconv.Quote(message))
	s.writeCUE(dir, src)
}

// TestNamedEnvironmentDefaultsAndState covers --env: the named file is
// picked up over the unnamed one, the default project name becomes
// "<dirname>-<name>", and state lands under .kevin/<name>/.
func (s *EnvSuite) TestNamedEnvironmentDefaultsAndState() {
	dir := s.T().TempDir()
	// project: "" behaves exactly like omitting the field - config.Config
	// defaults an empty decoded Project the same way either way - so this
	// still exercises the "<dirname>-<name>" default.
	src := fmt.Sprintf(oneStepCUE, "", strconv.Quote(s.echoPluginBin()), strconv.Quote("hi"))
	s.writeCUEFile(dir, "staging.kevin.cue", src)

	wantProject := filepath.Base(dir) + "-staging"

	out, code := s.runToCompletion(dir, "-C", dir, "--env", "staging", "validate")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, wantProject+": 0 setup step(s), 1 env step(s)", "default project name must be <dirname>-<name>")

	out, code = s.runUntil(dir, stepLine("a", "ready"), "-C", dir, "--env", "staging", "run")
	s.Equal(0, code, "output:\n%s", out)

	_, err := os.Stat(filepath.Join(dir, ".kevin", "staging", "logs.ndjson"))
	s.Require().NoError(err, "state must land under .kevin/staging/")
}

// TestKEVINEnvVariableSelectsTheSameEnvironment covers KEVIN_ENV as an
// alternative to --env.
func (s *EnvSuite) TestKEVINEnvVariableSelectsTheSameEnvironment() {
	dir := s.T().TempDir()
	s.writeOneStep(dir, "kevin-e2e-kevinenv-staging", "hi")
	require := s.Require()
	require.NoError(os.Rename(filepath.Join(dir, "kevin.cue"), filepath.Join(dir, "staging.kevin.cue")))

	cmd := s.startKevinWithEnv(dir, []string{"KEVIN_ENV=staging"}, "-C", dir, "run")
	s.waitFor(cmd, stepLine("a", "ready"), defaultTimeout)
	require.NoError(cmd.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(cmd, defaultTimeout)
	s.Equal(0, code, "output:\n%s", cmd.buf.String())
}

// TestTwoNamedEnvironmentsRunSimultaneously covers running the default and a
// named environment from the same directory at once, without colliding.
func (s *EnvSuite) TestTwoNamedEnvironmentsRunSimultaneously() {
	dir := s.T().TempDir()
	s.writeOneStep(dir, "kevin-e2e-simul-default", "default env")
	src := fmt.Sprintf(oneStepCUE, "kevin-e2e-simul-staging", strconv.Quote(s.echoPluginBin()), strconv.Quote("staging env"))
	s.writeCUEFile(dir, "staging.kevin.cue", src)
	require := s.Require()

	p1 := s.startKevin(dir, "-C", dir, "run")
	p2 := s.startKevin(dir, "-C", dir, "--env", "staging", "run")

	s.waitFor(p1, stepLine("a", "ready"), defaultTimeout)
	s.waitFor(p2, stepLine("a", "ready"), defaultTimeout)

	require.NoError(p1.cmd.Process.Signal(syscall.SIGINT))
	require.NoError(p2.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p1, defaultTimeout), "output:\n%s", p1.buf.String())
	s.Equal(0, s.waitExit(p2, defaultTimeout), "output:\n%s", p2.buf.String())

	_, err := os.Stat(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	_, err = os.Stat(filepath.Join(dir, ".kevin", "staging", "logs.ndjson"))
	s.Require().NoError(err)
}

// TestUnsetEnvVarFailsNonZero is the regression guard for the bug where a
// step whose CEL/env-var render failed was silently marked skipped instead
// of failed, with no error anywhere: an unset env.FOO reference must fail
// the step clearly and exit non-zero, not silently skip it.
func (s *EnvSuite) TestUnsetEnvVarFailsNonZero() {
	project := "kevin-e2e-cel-unset"
	dir := s.T().TempDir()
	s.writeOneStep(dir, project, "value is ${env.KEVIN_E2E_UNSET_VAR}")
	s.cleanupProject(project)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "failed:"), defaultTimeout)

	// a failed step still blocks for the interrupt rather than tearing
	// itself down automatically - the same behavior DAGSuite proves for a
	// plugin-level failure.
	time.Sleep(500 * time.Millisecond)
	s.True(s.running(p), "a failed render must still block for the interrupt")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(p, defaultTimeout)
	out := p.buf.String()
	s.NotEqual(0, code, "an unset env var must fail the run with a non-zero exit, not silently succeed, output:\n%s", out)
	s.Contains(out, stepLine("a", "failed:"))
}

// TestSetEnvVarSplicesCorrectly covers the success path: a set env.FOO
// splices its value into the with block.
func (s *EnvSuite) TestSetEnvVarSplicesCorrectly() {
	project := "kevin-e2e-cel-set"
	dir := s.T().TempDir()
	s.writeOneStep(dir, project, "value is ${env.KEVIN_E2E_SET_VAR}")
	s.cleanupProject(project)

	p := s.startKevinWithEnv(dir, []string{"KEVIN_E2E_SET_VAR=spliced-value"}, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "ready"), defaultTimeout)
	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())

	logs, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	s.Contains(string(logs), "value is spliced-value")
}

// TestHasFallbackForUnsetVar covers has(env.FOO) ? env.FOO : "default"
// falling back cleanly when the var is unset.
func (s *EnvSuite) TestHasFallbackForUnsetVar() {
	project := "kevin-e2e-cel-fallback"
	dir := s.T().TempDir()
	s.writeOneStep(dir, project,
		`value is ${has(env.KEVIN_E2E_UNSET_VAR_2) ? env.KEVIN_E2E_UNSET_VAR_2 : "localhost:5000"}`)
	s.cleanupProject(project)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "ready"), defaultTimeout)
	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())

	logs, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	s.Contains(string(logs), "value is localhost:5000")
}

// TestConnectRendersEnvTemplate is the regression guard for kevin connect
// sending a step's with block to Export completely unrendered: an
// ${env.VAR} reference must splice the real value, not the literal
// template, into what connect execs.
func (s *EnvSuite) TestConnectRendersEnvTemplate() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(`project: "kevin-e2e-connect-env"

plugins: echo: cmd: %s

env: a: {uses: "echo:echo", with: export: msg: "${env.KEVIN_E2E_CONNECT_ENV_VAR}"}
`, strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	out, code := s.runToCompletionWithEnv(dir, []string{"KEVIN_E2E_CONNECT_ENV_VAR=connect-value"}, "-C", dir, "connect", "a", "--", "env")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "msg=connect-value")
}

// TestConnectRendersProjectTemplate covers the same gap for
// ${project.root_cert} - a value connect can compute with no live DAG
// walk at all, unlike ${needs...}.
func (s *EnvSuite) TestConnectRendersProjectTemplate() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(`project: "kevin-e2e-connect-project"

plugins: echo: cmd: %s

env: a: {uses: "echo:echo", with: export: msg: "${project.root_cert}"}
`, strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	out, code := s.runToCompletion(dir, "-C", dir, "connect", "a", "--", "env")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "root.crt", "must render to the real CA path, not the literal ${project.root_cert} template")
	s.NotContains(out, "${project.root_cert}")
}

// TestConnectRendersSetupCrossScopeTemplate covers ${setup.<name>.out.<key>}
// through kevin connect, with no "kevin setup" ever having run first -
// Export is side-effect-free and needs no prior Up.
func (s *EnvSuite) TestConnectRendersSetupCrossScopeTemplate() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(`project: "kevin-e2e-connect-setup"

plugins: echo: cmd: %s

setup: base: {uses: "echo:echo", with: export: greeting: "from-setup"}
env: a: {
	uses:  "echo:echo"
	needs: ["setup.base"]
	with:  export: msg: "${setup.base.out.greeting}"
}
`, strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	out, code := s.runToCompletion(dir, "-C", dir, "connect", "a", "--", "env")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "msg=from-setup")
}

// TestProjectRootCertSplicesCorrectly covers the project.* CEL scope's
// root_cert entry through a normal "kevin run", the same shape as
// TestSetEnvVarSplicesCorrectly.
func (s *EnvSuite) TestProjectRootCertSplicesCorrectly() {
	project := "kevin-e2e-project-root-cert"
	dir := s.T().TempDir()
	s.writeOneStep(dir, project, "cert is ${project.root_cert}")
	s.cleanupProject(project)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("a", "ready"), defaultTimeout)
	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())

	logs, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	logStr := string(logs)
	s.Contains(logStr, "cert is ")
	s.Contains(logStr, "root.crt")
	s.NotContains(logStr, "${project.root_cert}")
}

// TestCrossScopeNeedsSurvivesSeparateProcesses covers docs/MANUAL_TESTING.md
// section 12's setup/env cross-scope case: "kevin setup" runs and exits in
// its own process - its plugin process is gone by the time a wholly
// separate "kevin run" process starts - and that later process still
// resolves needs: ["setup.<name>"] correctly, via a fresh Export call, not
// anything cached or persisted from the first process.
func (s *EnvSuite) TestCrossScopeNeedsSurvivesSeparateProcesses() {
	project := "kevin-e2e-cross-scope-needs"
	dir := s.T().TempDir()
	s.cleanupProject(project)

	src := fmt.Sprintf(`project: %s

plugins: echo: cmd: %s

setup: cluster: {
	uses: "echo:echo"
	with: {
		export:           {greeting: "from-setup", password: "hunter2"}
		export_sensitive: ["password"]
	}
}
env: app: {
	uses:  "echo:echo"
	needs: ["setup.cluster"]
	with:  message: "${setup.cluster.out.greeting}"
}
`, strconv.Quote(project), strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	out, code := s.runToCompletion(dir, "-C", dir, "setup")
	s.Equal(0, code, "kevin setup output:\n%s", out)
	s.Contains(out, stepLine("cluster", "ready"))

	out, code = s.runUntil(dir, stepLine("app", "ready"), "-C", dir, "run")
	s.Equal(0, code, "kevin run output:\n%s", out)

	logs, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	logStr := string(logs)
	s.Contains(logStr, "from-setup", "the CEL-rendered with block must carry the setup step's exported value")
	s.Contains(logStr, "saw setup.cluster:", "the wire Deps key must be the \"setup.\"-prefixed name")
	s.Contains(logStr, "password:[REDACTED]", "export_sensitive must keep its Sensitive flag crossing both scopes and processes")
	s.NotContains(logStr, "hunter2", "a Sensitive value must never appear in its raw form in the log")
}
