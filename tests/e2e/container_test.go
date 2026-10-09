//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// containerRelayCUE brings up a real nginx container with an expose entry
// routed through the environment's relay (relay: true) instead of a
// published host port. web_ready proves the "expose_web" system output (a
// socks5:// upstream) is dialable through the relay's SOCKS5 gateway, the
// same way examples/kind's apiserver_ready proves builtin:kubernetes's own
// expose entries. fetch then proves the "forward_web" system output (the
// plain host:port the engine's own local forward publishes on loopback) is
// dialable directly, with no SOCKS5 awareness needed - curl runs on the
// host via builtin:exec, not inside a container.
const containerRelayCUE = `project: "%s"

env: {
	web: {
		uses:  "builtin:container"
		label: "Web Server"
		with: {
			image:  "nginx:alpine"
			expose: web: {port: 80, relay: true}
		}
	}
	web_ready: {
		uses:  "builtin:wait"
		label: "Web Ready"
		needs: ["web"]
		with: tcp: address: "${needs.web.system.expose_web}"
	}
	fetch: {
		uses:  "builtin:exec"
		label: "Fetch"
		needs: ["web", "web_ready"]
		with: up: command: ["sh", "-c", "curl -s http://${needs.web.system.forward_web}/ | grep -o 'Welcome to nginx' | head -1"]
	}
	check: {
		uses:  "builtin:exec"
		label: "Check"
		needs: ["fetch"]
		with: up: command: ["sh", "-c", "echo BODY: ${needs.fetch.out.stdout}"]
	}
}
`

// ContainerRelaySuite covers a user exposing a container port through the
// relay: builtin:container's expose.relay - an expose entry that skips
// docker --publish and reaches the container through the project's relay
// instead.
//
// Tier: e2e.
type ContainerRelaySuite struct {
	e2eSuite
}

func TestContainerRelaySuite(t *testing.T) {
	suite.Run(t, new(ContainerRelaySuite))
}

// TestRelayEntryReachesContainer proves a relay: true expose entry is
// reachable both through the relay's SOCKS5 gateway (expose_web, checked by
// web_ready) and through the engine's own host-side forward (forward_web,
// checked by fetch/check).
func (s *ContainerRelaySuite) TestRelayEntryReachesContainer() {
	s.requireDocker()

	project := "kevin-e2e-container-relay"
	dir := s.project(project, containerRelayCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("check", "ready"), defaultTimeout)

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(p, defaultTimeout)
	s.Equal(0, code, "output:\n%s", p.buf.String())

	logs := s.readLogs(dir)
	s.Contains(logs, "BODY: ", "check step must have run")
	s.Contains(logs, "Welcome to nginx",
		"fetch must reach the real nginx container through the forward_web host:port")
}

// udpEchoStep is one socat container echoing every datagram, with its
// forward_echo address printed by an exec step for the test to dial.
const udpEchoStep = `
	%[1]s: {
		uses:  "builtin:container"
		with: {
			image: "alpine/socat:latest"
			cmd:   ["UDP4-RECVFROM:5000,fork", "EXEC:/bin/cat"]
			expose: echo: {port: 5000, protocol: "udp", relay: true}
		}
	}
	%[1]s_addr: {
		uses:  "builtin:exec"
		needs: ["%[1]s"]
		with: up: command: ["sh", "-c", "echo UDPADDR-%[1]s=${needs.%[1]s.system.forward_echo}"]
	}
`

// renderedProject writes a project whose env is steps, an already-rendered
// CUE fragment, and returns its directory.
func (s *ContainerRelaySuite) renderedProject(project, steps string) string {
	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+fmt.Sprintf("project: %q\n\nenv: {%s}\n", project, steps))
	s.cleanupProject(project)
	return dir
}

var udpAddrRE = regexp.MustCompile(`UDPADDR-(\w+)=([0-9.]+:\d+)`)

// udpAddr reads the forward_echo address the named step's exec printed,
// waiting for the durable log to catch up with the step's ready line.
func (s *ContainerRelaySuite) udpAddr(dir, name string) string {
	var addr string
	s.Require().Eventually(func() bool {
		b, err := os.ReadFile(filepath.Join(dir, ".kevin", "logs.ndjson"))
		if err != nil {
			return false
		}
		for _, m := range udpAddrRE.FindAllStringSubmatch(string(b), -1) {
			if m[1] == name {
				addr = m[2]
				return true
			}
		}
		return false
	}, 10*time.Second, 200*time.Millisecond, "no UDPADDR line for %s", name)
	return addr
}

// TestUDPRelayRoundTripsConcurrentClients proves a udp relay: true entry
// echoes through the forward_echo port, for two clients at once.
func (s *ContainerRelaySuite) TestUDPRelayRoundTripsConcurrentClients() {
	s.requireDocker()

	project := "kevin-e2e-container-udp"
	dir := s.renderedProject(project, fmt.Sprintf(udpEchoStep, "udp"))

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("udp_addr", "ready"), defaultTimeout)
	addr := s.udpAddr(dir, "udp")

	var wg sync.WaitGroup
	replies := make([]string, 2)
	for i := range replies {
		wg.Go(func() {
			var d net.Dialer
			conn, err := d.DialContext(s.T().Context(), "udp", addr)
			if err != nil {
				return
			}
			defer conn.Close() //nolint:errcheck // a UDP socket close has nothing to report
			msg := fmt.Sprintf("client-%d", i)
			buf := make([]byte, 64)
			// The relay's association may need a moment, so retry the send.
			for range 10 {
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := conn.Write([]byte(msg)); err != nil {
					return
				}
				if n, err := conn.Read(buf); err == nil {
					replies[i] = string(buf[:n])
					return
				}
			}
		})
	}
	wg.Wait()
	// The relay fans a reply out to every client, so a client may see the other's echo.
	for i, r := range replies {
		s.Contains([]string{"client-0", "client-1"}, r, "client %d must get an echo back", i)
	}

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(p, defaultTimeout), "output:\n%s", p.buf.String())
}

// TestUDPRelayPoolExhaustionFailsTheStep proves a pool of one UDP port
// refuses the second relay+udp entry instead of hanging.
func (s *ContainerRelaySuite) TestUDPRelayPoolExhaustionFailsTheStep() {
	s.requireDocker()

	project := "kevin-e2e-container-udp-pool"
	dir := s.renderedProject(project, fmt.Sprintf(udpEchoStep, "one")+fmt.Sprintf(udpEchoStep, "two"))

	p := s.startKevinWithEnv(dir, []string{"KEVIN_RELAY_UDP_POOL_SIZE=1"}, "-C", dir, "run")
	s.waitFor(p, "relay refused associate", defaultTimeout)
	s.Contains(p.buf.String(), "failed: local forward for echo")
	p.stop()
}
