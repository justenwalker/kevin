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
// docker resources.
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

// LifecycleSuite covers a user running an environment with kevin run: its
// basic lifecycle. Each test needs a differently shaped run (plain, --keep,
// --debug, crashed), so this suite sets up a fresh project per test method
// rather than sharing one SetupSuite bring-up.
//
// Tier: e2e.
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

	s.NotContains(out, "\x1b[", "a piped run must not draw the live display")
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

// routedCUE adds a route step and a second dependent to lifecycleCUE, so
// teardown order across siblings and a step with no Down are observable.
const routedCUE = `project: "%s"

env: {
	web: {
		uses: "builtin:container"
		with: {
			image:  "nginx:alpine"
			expose: web: {port: 80}
		}
	}
	web_route: {
		uses:  "builtin:route"
		needs: ["web"]
		with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
	}
	probe: {
		uses:  "builtin:container"
		needs: ["web"]
		with: {
			image: "busybox:stable"
			cmd:   ["sleep", "3600"]
		}
	}
	noproxy: {
		uses:  "builtin:container"
		needs: ["web"]
		with: {
			proxy: false
			image: "busybox:stable"
			cmd:   ["sleep", "3600"]
		}
	}
}
`

// TestTeardownRemovesEveryDependentBeforeItsDependency covers a step with
// two dependents and a route step: both dependents are removed before web,
// and the route step, which has nothing to tear down, prints no removal.
func (s *LifecycleSuite) TestTeardownRemovesEveryDependentBeforeItsDependency() {
	project := "kevin-e2e-lifecycle-routed"
	dir := s.project(project, routedCUE)

	out, code := s.runUntil(dir, stepLine("noproxy", "ready"), "-C", dir, "run")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, stepLine("web_route", "ready"))

	webRemoved := strings.Index(out, stepLine("web", "removed"))
	s.Require().NotEqual(-1, webRemoved, "web must be removed")
	for _, dependent := range []string{"probe", "noproxy"} {
		removed := strings.Index(out, stepLine(dependent, "removed"))
		s.Require().NotEqual(-1, removed, "%s must be removed", dependent)
		s.Less(removed, webRemoved, "%s must be torn down before web", dependent)
	}
	s.NotContains(out, stepLine("web_route", "removed"), "a route has nothing to remove")
}

// TestDebugFlagOnATerminalUsesThePlainStream covers --debug on a terminal:
// the live display is replaced by the plain line-per-event stream, at debug
// level.
func (s *LifecycleSuite) TestDebugFlagOnATerminalUsesThePlainStream() {
	project := "kevin-e2e-lifecycle-pty-debug"
	dir := s.project(project, lifecycleCUE)

	p := s.startKevinOnPTY(dir, "-C", dir, "--debug", "run")
	s.waitFor(p, stepLine("probe", "ready"), defaultTimeout)

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%q", p.buf.String())
	out := p.buf.String()

	s.NotRegexp(`\r\x1b\[\d+A\x1b\[J`, out, "--debug must not redraw the live list")
	s.Contains(out, " DEBUG ", "debug flag must produce debug-level log lines")
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

// TestCrashLeavesContainersAndSecondRunReconciles covers a crash: a
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

// TestDetachStartsInBackgroundAndStopTearsDown covers "kevin run --detach":
// the parent returns immediately with the console/proxy addresses, a
// second run against the same project fails fast against the live
// pidfile, and "kevin stop" signals the detached child and waits for its
// own teardown to remove the containers.
func (s *LifecycleSuite) TestDetachStartsInBackgroundAndStopTearsDown() {
	project := "kevin-e2e-lifecycle-detach"
	dir := s.project(project, lifecycleCUE)

	out, code := s.runToCompletion(dir, "-C", dir, "run", "--detach")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "started in background", "must report the detached pid/log")
	s.Contains(out, "console  http://", "must still print the console address once it's up")

	pidPath := filepath.Join(dir, ".kevin", "run", "kevin.pid")
	s.FileExists(pidPath, "a detached run must leave a pidfile behind")

	out, code = s.runToCompletion(dir, "-C", dir, "run")
	s.NotEqual(0, code, "a second run against the same project must fail fast")
	s.Contains(out, "already running", "output:\n%s", out)

	s.Eventually(func() bool {
		return len(s.containerIDsForProject(project)) >= 2
	}, defaultTimeout, 200*time.Millisecond, "the detached run must still bring both containers up")

	out, code = s.runToCompletion(dir, "-C", dir, "stop")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "stopped")

	s.NoFileExists(pidPath, "stop must remove the pidfile once the detached run exits")
	s.Empty(s.containerIDsForProject(project), "stop must wait for the detached run's own teardown")
}

// piped and dumb-terminal fallback: covered implicitly by every test above,
// since a subprocess's stdout/stderr piped into a syncBuffer is never a
// terminal, so wantsLiveUI is always false and kevin always falls back to
// the plain per-event stream that stepLine matches against.

// TestRunOnATerminalRedrawsTheStepList covers the live display: on a
// terminal kevin rewrites its step list in place with cursor-up and erase
// sequences, where a piped run (every other test here) gets plain lines.
func (s *LifecycleSuite) TestRunOnATerminalRedrawsTheStepList() {
	project := "kevin-e2e-lifecycle-pty"
	dir := s.project(project, lifecycleCUE)

	p := s.startKevinOnPTY(dir, "-C", dir, "run")
	s.waitFor(p, "2 ready", defaultTimeout)

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%q", p.buf.String())
	out := p.buf.String()

	s.Regexp(`\r\x1b\[\d+A\x1b\[J`, out, "the list must be redrawn with cursor-up and erase")
	s.Contains(out, "Web Server  running", "a running step must get its own row")
	s.NotContains(out, stepLine("web", "up"), "the plain event stream must not be written alongside the live list")
}
