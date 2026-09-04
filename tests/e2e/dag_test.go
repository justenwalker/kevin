//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// dagCUE mirrors examples/echo/kevin.cue: a fans out to b and c, d joins
// them, hold keeps the env up briefly, boom always fails, and e (which
// needs boom) must never start. hold's duration is shrunk from the
// example's 10s to keep the suite fast.
const dagCUE = `project: "%s"

plugins: echo: {
	cmd: %s
	config: greeting: "hello from the provider config"
}

env: {
	a: {
		uses:  "echo:echo"
		label: "Root"
		with: {
			message: "hello from a"
			outputs: greeting: "hi"
		}
	}
	b: {
		uses:  "echo:echo"
		label: "Parallel B"
		needs: ["a"]
		with: message: "b starts only after a is ready"
	}
	c: {
		uses:  "echo:echo"
		label: "Parallel C (delayed)"
		needs: ["a"]
		with: {
			message: "c runs at the same time as b"
			delay:   "300ms"
		}
	}
	d: {
		uses:  "echo:echo"
		label: "Join B+C"
		needs: ["b", "c"]
		with: message: "d waits for both b and c"
	}
	hold: {
		uses:  "builtin:wait"
		label: "Hold Open"
		needs: ["d"]
		with: duration: "1s"
	}
	boom: {
		uses:  "echo:fail"
		label: "Always Fails"
		needs: ["hold"]
	}
	e: {
		uses:  "echo:echo"
		label: "Never Runs"
		needs: ["boom"]
		with: message: "e never runs, because boom fails"
	}
}
`

// DAGSuite covers docs/MANUAL_TESTING.md section 6: DAG ordering and
// failure propagation. SetupSuite runs the DAG once to completion and
// captures its output/exit code; the TestXxx methods each assert a
// different facet of that one run.
type DAGSuite struct {
	e2eSuite

	output   string
	logs     string // .kevin/logs.ndjson: every step's own log lines
	exitCode int
}

func TestDAGSuite(t *testing.T) {
	suite.Run(t, new(DAGSuite))
}

func (s *DAGSuite) SetupSuite() {
	s.requireDocker()

	const project = "kevin-e2e-dag"
	dir := s.T().TempDir()
	src := fmt.Sprintf(dagCUE, project, strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, proxyBlock(s.T())+src)
	s.cleanupProject(project)

	p := s.startKevin(dir, "-C", dir, "run")

	// boom fails only after hold is up; wait for that, then confirm run
	// blocks for the interrupt instead of tearing itself down automatically
	// - this is the regression guard for the bug where a failed step made
	// the process exit 0 with no interrupt at all.
	s.waitFor(p, stepLine("boom", "failed:"), defaultTimeout)
	time.Sleep(1 * time.Second)
	s.Require().True(s.running(p), "kevin must still block for the interrupt after a step fails")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.exitCode = s.waitExit(p, defaultTimeout)
	s.output = p.buf.String()

	logData, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
	s.Require().NoError(err)
	s.logs = string(logData)
}

func (s *DAGSuite) TestFanOutFanInOrdering() {
	aReady := strings.Index(s.output, stepLine("a", "ready"))
	bUp := strings.Index(s.output, stepLine("b", "up"))
	cUp := strings.Index(s.output, stepLine("c", "up"))
	dUp := strings.Index(s.output, stepLine("d", "up"))
	bReady := strings.Index(s.output, stepLine("b", "ready"))
	cReady := strings.Index(s.output, stepLine("c", "ready"))

	s.Require().NotEqual(-1, aReady)
	s.Require().NotEqual(-1, bUp)
	s.Require().NotEqual(-1, cUp)
	s.Require().NotEqual(-1, dUp)

	s.Less(aReady, bUp, "b must start only after a is ready")
	s.Less(aReady, cUp, "c must start only after a is ready")
	s.Less(bReady, dUp, "d must wait for b")
	s.Less(cReady, dUp, "d must wait for c")
}

func (s *DAGSuite) TestENeverStarts() {
	s.NotContains(s.output, stepLine("e", "up"), "e depends on boom, which always fails, so e must never start")
}

// TestReverseTeardownOrderAfterFailure checks d, b, c, a are torn down in
// reverse dependency order. hold has no observable "removed" line of its
// own - builtin:wait implements no Down, so the engine marks it Removed in
// the console without emitting terminal text for it - but its Up (and
// boom's failure right after) already proved it came up first.
func (s *DAGSuite) TestReverseTeardownOrderAfterFailure() {
	dRemoved := strings.Index(s.output, stepLine("d", "removed"))
	bRemoved := strings.Index(s.output, stepLine("b", "removed"))
	cRemoved := strings.Index(s.output, stepLine("c", "removed"))
	aRemoved := strings.Index(s.output, stepLine("a", "removed"))

	s.Require().NotEqual(-1, dRemoved, "d must be removed")
	s.Require().NotEqual(-1, bRemoved, "b must be removed")
	s.Require().NotEqual(-1, cRemoved, "c must be removed")
	s.Require().NotEqual(-1, aRemoved, "a must be removed")

	s.Less(dRemoved, bRemoved, "d must be torn down before its dependency b")
	s.Less(dRemoved, cRemoved, "d must be torn down before its dependency c")
	s.Less(bRemoved, aRemoved, "b must be torn down before a, the root")
	s.Less(cRemoved, aRemoved, "c must be torn down before a, the root")
}

// TestExitCodeIsNonZero is the regression guard for the bug where
// cmd/kevin/main.go forced exit code 0 on any interrupt regardless of the
// actual error - a failed step must still produce a non-zero exit.
func (s *DAGSuite) TestExitCodeIsNonZero() {
	s.Equal(1, s.exitCode)
}

// TestProviderConfigAndStepOutputDelivery covers cross-step values and
// provider-level Configure delivery. A step's own log lines never reach the
// terminal (see onEvent's comment in internal/engine/engine.go) - only the
// console and .kevin/logs.ndjson - so this reads the ndjson file rather
// than the process output: a's step-level output (outputs: greeting: "hi")
// reaches b through req.Deps, and the provider-level config.greeting set
// once via Configure shows up in every step's log.
func (s *DAGSuite) TestProviderConfigAndStepOutputDelivery() {
	s.Contains(s.logs, "provider greeting: hello from the provider config")

	sawA := strings.Index(s.logs, "saw a:")
	s.Require().NotEqual(-1, sawA, "b or c must log the dependency output it saw from a")
	line := s.logs[sawA : sawA+200]
	s.Contains(line, "hi", "a's step-level output (outputs: greeting: \"hi\") must reach its dependent")
}
