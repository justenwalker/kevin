//go:build integration

package relay_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

// relayProject names every docker resource that this suite creates. The name
// stays unique across the integration suites so two suites never collide.
const relayProject = "kevin-it-relay"

// relayDomain is the environment domain that the relay answers for.
const relayDomain = "kevin.home"

// relayImageTag matches RelayImageTag in build/main.go.
const relayImageTag = "kevin-relay:dev"

// RelaySuite drives one relay container against a real docker daemon.
type RelaySuite struct {
	suite.Suite

	network string
	relay   *relay.Relay
}

func TestRelaySuite(t *testing.T) {
	suite.Run(t, new(RelaySuite))
}

// SetupSuite creates the shared network and starts the relay once for every
// test in the suite.
func (s *RelaySuite) SetupSuite() {
	t := s.T()
	if err := dockerClient.Available(t.Context()); err != nil {
		t.Skip("docker is unavailable:", err)
	}
	ensureRelayImage(t)

	s.network = "kevin-" + relayProject
	s.Require().NoError(dockerClient.NetworkCreate(t.Context(), s.network, map[string]string{
		cri.LabelProject: relayProject,
	}))

	r, err := relay.Start(t.Context(), relay.Options{
		Project:   relayProject,
		Network:   s.network,
		Domain:    relayDomain,
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
	})
	s.Require().NoError(err)
	s.relay = r
}

// TearDownSuite removes the relay and the network, even when a test failed.
func (s *RelaySuite) TearDownSuite() {
	if s.relay != nil {
		s.Require().NoError(s.relay.Close())
	}
	s.Require().NoError(dockerClient.NetworkRemove(context.WithoutCancel(context.Background()), s.network))
}

func (s *RelaySuite) containerName() string {
	return "kevin-" + relayProject + "-relay"
}

// TestAddrIsRoutableOnTheNetwork proves that Addr reports the address that
// the relay container carries on the shared network.
func (s *RelaySuite) TestAddrIsRoutableOnTheNetwork() {
	t := s.T()
	info, err := dockerClient.Inspect(t.Context(), s.containerName())
	s.Require().NoError(err)

	s.Equal(info.IPs[s.network], s.relay.Addr(),
		"Addr must report the container address on the shared network")
	s.Regexp(`^\d+\.\d+\.\d+\.\d+$`, s.relay.Addr())
}

// TestCarriesRoleAndProjectLabels proves that the relay container carries
// the labels that mark its role and its owner.
func (s *RelaySuite) TestCarriesRoleAndProjectLabels() {
	t := s.T()
	info, err := dockerClient.Inspect(t.Context(), s.containerName())
	s.Require().NoError(err)

	s.Equal(relay.Role, info.Labels[cri.LabelRole], "the relay container must carry the role label")
	s.Equal(relayProject, info.Labels[cri.LabelProject], "the relay container must carry the project label")
}

// TestRelayAnswersDNSForANameUnderTheDomain proves that a workload on the
// shared network resolves a name under the domain to the relay address.
func (s *RelaySuite) TestRelayAnswersDNSForANameUnderTheDomain() {
	t := s.T()
	ctx := t.Context()

	name := relayProject + "-nslookup"
	_, err := dockerClient.Run(ctx, cri.RunSpec{
		Image:   "busybox:stable",
		Name:    name,
		Network: s.network,
		DNS:     []string{s.relay.Addr()},
		Cmd:     []string{"sleep", "300"},
	})
	s.Require().NoError(err)
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(context.Background()), name) })

	out, err := dockerClient.Exec(ctx, name, "nslookup", "app."+relayDomain)
	s.Require().NoError(err)
	s.Contains(out, s.relay.Addr(), "a name under the domain must resolve to the relay address")
}

// TestCloseIsIdempotent starts a throwaway relay and proves that a second
// Close is not an error. The shared suite relay stays up for the other
// tests, so this test manages its own instance.
func (s *RelaySuite) TestCloseIsIdempotent() {
	t := s.T()
	ctx := t.Context()

	project := relayProject + "-close"
	r, err := relay.Start(ctx, relay.Options{
		Project:   project,
		Network:   s.network,
		Domain:    relayDomain,
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
	})
	s.Require().NoError(err)

	s.Require().NoError(r.Close())
	name := "kevin-" + project + "-relay"
	_, err = dockerClient.Inspect(ctx, name)
	s.Require().ErrorIs(err, cri.ErrNotFound, "Close must remove the container")

	s.Require().NoError(r.Close(), "a second Close must not be an error")
}

// TestRefPrecedenceAgainstARealEnvironment proves that KEVIN_RELAY_IMAGE in
// the process environment wins over a configured image.
func (s *RelaySuite) TestRefPrecedenceAgainstARealEnvironment() {
	t := s.T()
	t.Setenv(relay.ImageEnvVar, "from-real-env:dev")

	s.Equal("from-real-env:dev", relay.Ref("from-config:dev"))
}

// ensureRelayImage builds the relay image from source when it is absent, the
// same way as the relay-image build target. It skips the suite when it
// cannot build the image.
func ensureRelayImage(t *testing.T) {
	t.Helper()

	check := exec.CommandContext(t.Context(), "docker", "image", "inspect", relayImageTag)
	if check.Run() == nil {
		return
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate the repository root to build the relay image")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")

	dir := t.TempDir()
	bin := filepath.Join(dir, "kevin-relay")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/kevin-relay")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skip("cannot build kevin-relay for the image:", err, string(out))
	}

	dockerBuild := exec.CommandContext(t.Context(), "docker", "build",
		"-f", filepath.Join(root, "build", "relay.Dockerfile"), "-t", relayImageTag, dir)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		t.Skip("cannot build the relay image:", err, string(out))
	}
}
