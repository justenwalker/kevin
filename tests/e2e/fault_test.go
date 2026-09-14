//go:build e2e

package e2e

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// faultCUE brings up a real http-echo container, published through
// builtin:container's own expose mechanism (host_5678, an OS-assigned
// port - never a fixed one, to stay collision-safe under parallel e2e
// runs). probe_before proves it's reachable before any fault is applied.
// backend_fault then sets loss_percent: 100 on backend's own network
// namespace - not a probabilistic rate, so probe_after's failure is
// deterministic, not flaky: every packet backend's own interface would
// send out (including a TCP handshake's SYN-ACK) is dropped, so a client
// connection to it just times out.
const faultCUE = `project: "%s"

env: {
	backend: {
		uses:  "builtin:container"
		label: "Backend"
		with: {
			image: "hashicorp/http-echo"
			cmd: ["-text=hello", "-listen=:5678"]
			expose: backend: {port: 5678}
		}
	}
	backend_ready: {
		uses:  "builtin:wait"
		label: "Backend Ready"
		needs: ["backend"]
		with: tcp: address: "${needs.backend.out.host_5678}"
	}
	probe_before: {
		uses:  "builtin:exec"
		label: "Probe (before fault)"
		needs: ["backend", "backend_ready"]
		with: up: command: ["sh", "-c", "curl -sf -m 2 http://${needs.backend.out.host_5678}/ >/dev/null && echo REACHED || echo BLOCKED"]
	}
	backend_fault: {
		uses:  "builtin:fault"
		label: "Network Fault"
		needs: ["backend", "probe_before"]
		with: {
			loss_percent: 100.0
		}
	}
	probe_after: {
		uses:  "builtin:exec"
		label: "Probe (after fault)"
		needs: ["backend", "backend_fault"]
		with: up: command: ["sh", "-c", "curl -sf -m 2 http://${needs.backend.out.host_5678}/ >/dev/null && echo REACHED || echo BLOCKED"]
	}
	check: {
		uses:  "builtin:exec"
		label: "Check"
		needs: ["probe_before", "probe_after"]
		with: up: command: ["sh", "-c", "echo BEFORE: ${needs.probe_before.out.stdout} AFTER: ${needs.probe_after.out.stdout}"]
	}
}
`

// FaultSuite covers builtin:fault end to end against a real docker
// container and a real relay - the one path a unit test can't reach,
// since internal/relay.Relay's fields are unexported outside its own
// package, and internal/engine's own tests have no way to fake it.
type FaultSuite struct {
	e2eSuite
}

func TestFaultSuite(t *testing.T) {
	suite.Run(t, new(FaultSuite))
}

// TestFaultBlocksTrafficThenClearsOnTeardown proves ApplyFault actually
// reaches the relay and takes effect (backend is reachable before, and
// not after), and that ClearFault runs on the way down without erroring
// the shutdown.
func (s *FaultSuite) TestFaultBlocksTrafficThenClearsOnTeardown() {
	s.requireDocker()

	project := "kevin-e2e-fault"
	dir := s.project(project, faultCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("check", "ready"), defaultTimeout)

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(p, defaultTimeout)
	s.Equal(0, code, "output:\n%s", p.buf.String())

	logs := s.readLogs(dir)
	s.Contains(logs, "BEFORE: REACHED", "backend must be reachable before backend_fault comes up")
	s.Contains(logs, "AFTER: BLOCKED", "loss_percent: 100 must block backend's own traffic once applied")
}
