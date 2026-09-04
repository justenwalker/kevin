//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// lifecycleCUE brings up a real container (web) and a dependent (probe), so
// teardown order and --keep/crash survival are observable against real
// docker resources - matches docs/MANUAL_TESTING.md section 1.
const lifecycleCUE = `project: "%s"

env: {
	web: {
		uses:  "builtin:container"
		label: "Web Server"
		with: {
			image:  "nginx:alpine"
			expose: web: {port: 80}
		}
	}
	probe: {
		uses:  "builtin:container"
		label: "Probe"
		needs: ["web"]
		with: {
			image: "busybox:stable"
			cmd:   ["sleep", "3600"]
		}
	}
}
`

// LifecycleSuite covers docs/MANUAL_TESTING.md section 1: basic env
// lifecycle. Each test needs a differently shaped run (plain, --keep,
// --debug, crashed), so this suite sets up a fresh project per test method
// rather than sharing one SetupSuite bring-up.
type LifecycleSuite struct {
	e2eSuite
}

func TestLifecycleSuite(t *testing.T) {
	suite.Run(t, new(LifecycleSuite))
}

func (s *LifecycleSuite) SetupTest() {
	s.requireDocker()
}

// TestRunPrintsAddressesAndTearsDownInReverseOrder covers the plain "kevin
// run" path: the address/hint lines, both steps reaching ready, and Ctrl-C
// removing probe before web with no containers left behind.
func (s *LifecycleSuite) TestRunPrintsAddressesAndTearsDownInReverseOrder() {
	project := "kevin-e2e-lifecycle"
	dir := s.project(project, lifecycleCUE)

	out, code := s.runUntil(dir, stepLine("probe", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)

	s.Contains(out, "console  http://", "must print the console address")
	s.Contains(out, "proxy    http://", "must print the proxy address")
	s.Contains(out, "export HTTP_PROXY=", "must print the shell hint")
	s.Contains(out, stepLine("web", "ready"))

	probeRemoved := strings.Index(out, stepLine("probe", "removed"))
	webRemoved := strings.Index(out, stepLine("web", "removed"))
	s.Require().NotEqual(-1, probeRemoved, "probe must be removed")
	s.Require().NotEqual(-1, webRemoved, "web must be removed")
	s.Less(probeRemoved, webRemoved, "probe (the dependent) must be torn down before web")

	s.Empty(s.containerIDsForProject(project), "no container may remain after a plain run")
}

// TestDebugFlagLogsAtDebugLevel covers --debug: it falls back to the plain
// stream (proven implicitly - runUntil relies on that) and logs at debug
// level.
func (s *LifecycleSuite) TestDebugFlagLogsAtDebugLevel() {
	project := "kevin-e2e-lifecycle-debug"
	dir := s.project(project, lifecycleCUE)

	out, code := s.runUntil(dir, stepLine("probe", "ready"), "-C", dir, "--debug", "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, " DEBUG ", "debug flag must produce debug-level log lines")
}

// TestKevinLogHasDebugJSONLines covers the .kevin/kevin.log file: it exists
// and carries full JSON lines at debug level even without --debug on the
// terminal.
func (s *LifecycleSuite) TestKevinLogHasDebugJSONLines() {
	project := "kevin-e2e-lifecycle-log"
	dir := s.project(project, lifecycleCUE)

	_, code := s.runUntil(dir, stepLine("probe", "ready"), "-C", dir, "run")
	s.Equal(0, code)

	logPath := filepath.Join(dir, ".kevin", "kevin.log")
	data, err := os.ReadFile(logPath)
	s.Require().NoError(err, "kevin.log must exist")

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	s.Require().NotEmpty(lines)
	sawDebug := false
	for _, line := range lines {
		s.True(strings.HasPrefix(line, "{"), "kevin.log lines must be JSON: %q", line)
		if strings.Contains(line, `"level":"DEBUG"`) {
			sawDebug = true
		}
	}
	s.True(sawDebug, "kevin.log must contain debug-level lines even without --debug on the terminal")
}

// TestKeepBlocksForInterruptAndLeavesContainers is the regression guard for
// the bug where "kevin run --keep" returned immediately instead of blocking
// for Ctrl-C: it must still be running several seconds after reaching
// ready, and on interrupt it must leave the containers running.
func (s *LifecycleSuite) TestKeepBlocksForInterruptAndLeavesContainers() {
	project := "kevin-e2e-lifecycle-keep"
	dir := s.project(project, lifecycleCUE)

	p := s.startKevin(dir, "-C", dir, "run", "--keep")
	s.waitFor(p, stepLine("probe", "ready"), defaultTimeout)

	time.Sleep(3 * time.Second)
	s.True(s.running(p), "kevin run --keep must still be blocked for the interrupt, not have exited early")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(p, defaultTimeout)
	s.Equal(0, code, "output:\n%s", p.buf.String())

	s.NotEmpty(s.containerIDsForProject(project), "--keep must leave the containers running")
}

// TestCrashLeavesContainersAndSecondRunReconciles covers section 16: a
// SIGKILL (a real crash, not Ctrl-C) leaves the containers running, and a
// second run afterward still succeeds - state is derived from live docker
// labels, not a state file.
func (s *LifecycleSuite) TestCrashLeavesContainersAndSecondRunReconciles() {
	project := "kevin-e2e-lifecycle-crash"
	dir := s.project(project, lifecycleCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("probe", "ready"), defaultTimeout)
	s.sigkill(p)

	s.NotEmpty(s.containerIDsForProject(project), "a crash must leave the containers running")

	out, code := s.runUntil(dir, stepLine("probe", "ready"), "-C", dir, "run")
	s.Equal(0, code, "a second run after a crash must still succeed, output:\n%s", out)

	s.Empty(s.containerIDsForProject(project), "the second run's own teardown must still leave nothing behind")
}

// piped and dumb-terminal fallback: covered implicitly by every test above,
// since a subprocess's stdout/stderr piped into a syncBuffer is never a
// terminal, so wantsLiveUI is always false and kevin always falls back to
// the plain per-event stream that stepLine matches against.
