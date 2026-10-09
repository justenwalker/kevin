//go:build integration

package relay_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/relay/relaytest"
)

// relayProject names every docker resource that this suite creates. The name
// stays unique across the integration suites so two suites never collide.
const relayProject = "kevin-it-relay"

// relayDomain is the environment domain that the relay answers for.
const relayDomain = "kevin.home"

// RelaySuite drives one relay container against a real docker daemon.
//
// Tier: integration.
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
	relaytest.UseDevImage(t)

	s.network = "kevin-" + relayProject
	s.Require().NoError(dockerClient.NetworkCreate(t.Context(), s.network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: relayProject},
	}))

	r, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   relayProject,
		Network:   s.network,
		Domain:    relayDomain,
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
		Authority: newTestAuthority(t),
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

// TestSOCKS5AddrIsPublishedOnLoopback proves that SOCKS5Addr reports the
// real published address of the relay's SOCKS5 gateway, against the actual
// kevin-relay image - relay_test.go's TestStartAndClose checks the same
// thing against a fixture image that never runs the real binary.
func (s *RelaySuite) TestSOCKS5AddrIsPublishedOnLoopback() {
	t := s.T()
	info, err := dockerClient.Inspect(t.Context(), s.containerName())
	s.Require().NoError(err)

	s.Equal(info.Ports["1080/tcp"], s.relay.SOCKS5Addr(),
		"SOCKS5Addr must report the loopback address docker published the gateway on")
	s.Regexp(`^127\.0\.0\.1:\d+$`, s.relay.SOCKS5Addr())
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
