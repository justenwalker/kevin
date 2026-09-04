//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// relayDevImageOnce builds kevin-relay:dev once for the whole run.
var relayDevImageOnce = sync.OnceValues(buildRelayDevImage)

// buildRelayDevImage cross-compiles kevin-relay for linux/GOARCH and builds
// it into kevin-relay:dev, the same way build/main.go's relay-image gnob
// target does - reimplemented here, self-contained, so "go test -tags e2e"
// needs no gnob bootstrap first.
//
// This is required, not optional, whenever internal/relay's own
// version-derived default (relay.Image) would otherwise resolve to the
// ghcr.io image matching this checkout's internal/version/VERSION - a real
// released tag that predates whatever relay feature is still unreleased on
// this branch, container relay routing included.
func buildRelayDevImage() (string, error) {
	dir, err := os.MkdirTemp("", "kevin-e2e-relay-image")
	if err != nil {
		return "", fmt.Errorf("e2e: mkdir temp: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best effort cleanup of a temp directory

	bin := filepath.Join(dir, "linux", runtime.GOARCH, "kevin-relay")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}

	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/kevin-relay")
	build.Dir = repoRoot()
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("e2e: build kevin-relay: %w: %s", err, out)
	}

	const tag = "kevin-relay:dev"
	dockerBuild := exec.CommandContext(context.Background(), "docker", "build",
		"-f", filepath.Join(repoRoot(), "build", "relay.Dockerfile"),
		"--build-arg", "TARGETARCH="+runtime.GOARCH,
		"-t", tag, dir)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		return "", fmt.Errorf("e2e: docker build kevin-relay image: %w: %s", err, out)
	}
	return tag, nil
}

// containerRelayCUE brings up a real nginx container with an expose entry
// routed through the environment's relay (relay: true) instead of a
// published host port. web_ready proves the "expose_web" system output (a
// socks5:// upstream) is dialable through the relay's SOCKS5 gateway, the
// same way examples/kind's apiserver_ready proves builtin:kind's own
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

// ContainerRelaySuite covers docs/MANUAL_TESTING.md section 19:
// builtin:container's expose.relay - an expose entry that skips
// docker --publish and reaches the container through the project's relay
// instead.
type ContainerRelaySuite struct {
	e2eSuite
}

func TestContainerRelaySuite(t *testing.T) {
	suite.Run(t, new(ContainerRelaySuite))
}

// TestRelayEntryReachesContainerAndSkipsPublish proves a relay: true expose
// entry never publishes a host port, yet is reachable both through the
// relay's SOCKS5 gateway (expose_web, checked by web_ready) and through the
// engine's own host-side forward (forward_web, checked by fetch/check).
func (s *ContainerRelaySuite) TestRelayEntryReachesContainerAndSkipsPublish() {
	s.requireDocker()

	relayImage, err := relayDevImageOnce()
	s.Require().NoError(err)

	project := "kevin-e2e-container-relay"
	dir := s.project(project, containerRelayCUE)

	p := s.startKevinWithEnv(dir, []string{"KEVIN_RELAY_IMAGE=" + relayImage}, "-C", dir, "run")
	s.waitFor(p, stepLine("check", "ready"), defaultTimeout)

	// Checked while still up - Down removes the container, taking its
	// published-port state with it.
	s.assertNoPublishedPorts("kevin-" + project + "-web")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	code := s.waitExit(p, defaultTimeout)
	s.Equal(0, code, "output:\n%s", p.buf.String())

	logs := s.readLogs(dir)
	s.Contains(logs, "BODY: ", "check step must have run")
	s.Contains(logs, "Welcome to nginx",
		"fetch must reach the real nginx container through the forward_web host:port")
}

// assertNoPublishedPorts confirms a relay-routed container never got a
// docker --publish entry for its exposed port - reachability comes only
// through the relay, not a host-bound port.
func (s *ContainerRelaySuite) assertNoPublishedPorts(container string) {
	t := s.T()
	t.Helper()

	out, err := exec.CommandContext(context.Background(), "docker", "inspect",
		"--format", "{{json .NetworkSettings.Ports}}", container).Output()
	s.Require().NoError(err)
	s.NotContains(string(out), "HostPort",
		"a relay entry must never appear in the docker --publish list, got: %s", out)
}
