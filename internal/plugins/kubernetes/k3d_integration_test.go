//go:build integration

package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"golang.org/x/net/proxy"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/k3dcmd"
	"github.com/justenwalker/kevin/internal/podman"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/plugin"
)

// k3dProject names every docker and k3d resource that this suite creates, so
// the suite never collides with a developer's own cluster or another suite.
const k3dProject = "k3d-it"

// k3dDomain is the environment domain that CoreDNS forwards to the relay.
const k3dDomain = "kevin.home"

// k3dStepName is the step name that the suite passes to Up and Down.
const k3dStepName = "cluster"

// k3dMountPath is where the suite mounts a host directory in every node.
const k3dMountPath = "/mnt/host"

// configJSON is the with block of the suite cluster: one worker, a relay for
// the API server, and a host directory mounted in every node.
func (s *K3dSuite) configJSON() string {
	return fmt.Sprintf(`{"driver":"k3d","workers":{"worker":{}},"expose":{"apiserver":{"address":"kubernetes.default.svc:443"}},"mounts":[{"host":%q,"container":%q,"readonly":true}]}`,
		s.mountDir, k3dMountPath)
}

// K3dSuite drives one k3d cluster against a real docker daemon. The suite
// creates a single cluster and asserts everything against it.
type K3dSuite struct {
	suite.Suite

	network     string
	workspace   string
	caPEM       string
	relay       *relay.Relay
	clusterName string
	kubeconfig  string
	mountDir    string
	up          *plugin.Result
}

func TestK3dSuite(t *testing.T) {
	suite.Run(t, new(K3dSuite))
}

// env is the plugin environment that Up and Down receive.
func (s *K3dSuite) env() plugin.Env {
	return plugin.Env{
		Project:   k3dProject,
		Workspace: s.workspace,
		Network:   s.network,
		CAPath:    ca.RootCertPath(),
		Domain:    k3dDomain,
		Relay:     s.relay.Addr(),
	}
}

// SetupSuite creates the shared network, starts the relay, generates a real
// certificate authority, and creates the cluster once for every test in the
// suite.
func (s *K3dSuite) SetupSuite() {
	t := s.T()
	requireDocker(t)
	requireK3d(t)
	ensureRelayImage(t)

	s.network = "kevin-" + k3dProject
	s.Require().NoError(dockerClient.NetworkCreate(t.Context(), s.network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: k3dProject},
	}))

	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", k3dProject, ca.Options{})
	_, err := m.LoadOrGenerateRoot()
	s.Require().NoError(err)
	intermediate, err := m.LoadOrGenerateIntermediate()
	s.Require().NoError(err)
	s.caPEM = intermediate.RootPEM()

	r, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   k3dProject,
		Network:   s.network,
		Domain:    k3dDomain,
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
		Authority: intermediate,
	})
	s.Require().NoError(err)
	s.relay = r

	s.workspace = t.TempDir()
	s.mountDir = t.TempDir()
	s.Require().NoError(os.WriteFile(filepath.Join(s.mountDir, "probe.txt"), []byte("from the host"), 0o600))
	res, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   k3dStepName,
		Env:    s.env(),
		Config: []byte(s.configJSON()),
	}, &capture{})
	s.Require().NoError(err, "Up must create the cluster")
	s.up = res
	s.clusterName = res.Outputs["name"].Reveal()
	s.kubeconfig = res.Outputs["kubeconfig"].Reveal()
}

// TearDownSuite removes the cluster, the relay, and the network, even when an
// earlier removal failed.
func (s *K3dSuite) TearDownSuite() {
	t := s.T()
	ctx := context.WithoutCancel(context.Background())

	if s.clusterName != "" {
		downErr := Step{}.Down(t.Context(), &plugin.DownRequest{
			Step:   k3dStepName,
			Env:    plugin.Env{Project: k3dProject, Workspace: s.workspace},
			Config: []byte(s.configJSON()),
		}, &capture{})
		s.NoError(downErr, "Down must remove the cluster without error")
		if downErr != nil {
			_ = k3dcmd.Delete(ctx, s.clusterName, nil, os.Stderr)
		}
		nodes, err := k3dcmd.GetNodes(ctx, s.clusterName, nil)
		s.NoError(err)
		s.Empty(nodes, "Down must remove every node")
		_, gwErr := dockerClient.NetworkGateway(ctx, "kevin-k3d-"+s.clusterName)
		s.ErrorIs(gwErr, cri.ErrNotFound, "Down must remove the network that Create made")
	}

	if s.relay != nil {
		s.NoError(s.relay.Close())
	}
	s.NoError(dockerClient.NetworkRemove(ctx, s.network))
}

// k3dDriver returns the driver for the suite cluster.
func (s *K3dSuite) k3dDriver() *k3dDriver {
	drv, err := newK3dDriver(config{}, plugin.Env{}, s.clusterName, s.kubeconfig, dockerClient)
	s.Require().NoError(err)
	return drv
}

// nodeList returns the node names Up published: the server first, then each
// agent.
func (s *K3dSuite) nodeList() []string {
	return strings.Split(s.up.Outputs["nodes"].Reveal(), ",")
}

// TestUpPublishesWhatADependentStepNeeds proves that Up returns a kubeconfig
// path that exists, the k3d context name, and the server and agent nodes.
func (s *K3dSuite) TestUpPublishesWhatADependentStepNeeds() {
	_, err := os.Stat(s.kubeconfig)
	s.Require().NoError(err, "the kubeconfig path that Up publishes must exist")

	s.Equal("k3d-"+s.clusterName, s.up.Outputs["context"].Reveal())
	s.Equal([]string{"k3d-" + s.clusterName + "-server-0", "k3d-" + s.clusterName + "-agent-0"}, s.nodeList())
}

// TestAPIServerListensOnLoopbackOnly proves that the kubeconfig names a
// loopback address, and that k3d publishes no port on another interface.
func (s *K3dSuite) TestAPIServerListensOnLoopbackOnly() {
	t := s.T()
	raw, err := os.ReadFile(s.kubeconfig)
	s.Require().NoError(err)
	s.Contains(string(raw), "server: https://127.0.0.1:")

	lb := "k3d-" + s.clusterName + "-serverlb"
	out, err := exec.CommandContext(t.Context(), "docker", "port", lb).Output()
	s.Require().NoError(err)
	s.NotEmpty(out)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		s.Contains(line, "-> 127.0.0.1:", "a published port must bind loopback, not every interface")
	}
}

// TestNodesJoinedTheSharedNetwork proves that every node joined the docker
// network of the suite, and that the network carries its default route.
func (s *K3dSuite) TestNodesJoinedTheSharedNetwork() {
	t := s.T()
	for _, node := range s.nodeList() {
		info, err := dockerClient.Inspect(t.Context(), node)
		s.Require().NoError(err)
		s.Contains(info.IPs, s.network)
		s.Contains(info.IPs, "kevin-k3d-"+s.clusterName)
		requireDefaultRoute(t, node, s.network)
	}
}

// TestNodesCarryTheirKevinLabel proves that the server is labelled
// "control-plane" and the agent by its workers key.
func (s *K3dSuite) TestNodesCarryTheirKevinLabel() {
	out, err := s.k3dDriver().Kubectl(s.T().Context(), "get", "nodes", "-L", nodeLabelKey, "--no-headers")
	s.Require().NoError(err)

	s.Contains(out, "k3d-"+s.clusterName+"-server-0")
	s.Contains(out, controlPlaneNodeName)
	s.Contains(out, "k3d-"+s.clusterName+"-agent-0")
	s.Contains(out, "worker")
}

// TestCoreDNSCarriesTheForwardZone proves that Up patches CoreDNS with a
// forward zone for the domain, and that the original zone survives.
func (s *K3dSuite) TestCoreDNSCarriesTheForwardZone() {
	out, err := s.k3dDriver().Kubectl(s.T().Context(), "-n", "kube-system", "get", "configmap", "coredns-custom",
		"-o", "jsonpath={.data.kevin\\.server}")
	s.Require().NoError(err)

	s.Contains(out, k3dDomain+":53 {")
	s.Contains(out, "forward . "+s.relay.Addr())
}

// TestNodeDNSPointsAtRelay proves that Up rewrites every node's own
// resolv.conf to name the relay as its only nameserver.
func (s *K3dSuite) TestNodeDNSPointsAtRelay() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", "/etc/resolv.conf")
		s.Require().NoError(err)
		s.Contains(out, "nameserver "+s.relay.Addr())
	}
}

// TestContainersReportOnePerNode proves that Up populates Containers with one
// entry per node, carrying the k3s pod and service subnets, and the server's
// Name carrying its kevin.node label.
func (s *K3dSuite) TestContainersReportOnePerNode() {
	s.Require().Len(s.up.Containers, len(s.nodeList()))
	for _, c := range s.up.Containers {
		s.NotEmpty(c.NetnsPath)
		s.Equal([]string{"10.42.0.0/16", "10.43.0.0/16"}, c.ExcludeCIDRs)
	}
	s.Equal(controlPlaneNodeName, s.up.Containers[0].Name)
}

// TestClusterUsesTheReportedSubnets proves that the cluster runs on the pod
// and service subnets that Up reports for capture.
func (s *K3dSuite) TestClusterUsesTheReportedSubnets() {
	t := s.T()
	pod, err := s.k3dDriver().Kubectl(t.Context(), "get", "node", "-o", "jsonpath={.items[*].spec.podCIDR}")
	s.Require().NoError(err)
	for cidr := range strings.FieldsSeq(pod) {
		s.True(strings.HasPrefix(cidr, "10.42."), "node pod CIDR %s is inside %s", cidr, k3sPodCIDR)
	}

	service, err := s.k3dDriver().Kubectl(t.Context(), "get", "service", "kubernetes", "-o", "jsonpath={.spec.clusterIP}")
	s.Require().NoError(err)
	s.True(strings.HasPrefix(strings.TrimSpace(service), "10.43."), "service IP %s is inside %s", service, k3sServiceCIDR)
}

// TestNodeHoldsTheKevinRoot proves that Create mounted the kevin root
// certificate into every node.
func (s *K3dSuite) TestNodeHoldsTheKevinRoot() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", k3dCAPath)
		s.Require().NoError(err)
		s.Contains(normalizePEM(out), normalizePEM(s.caPEM))
	}
}

// TestMountsReachEveryNode proves that the mounted host directory is visible
// in the server and the agent, and read-only.
func (s *K3dSuite) TestMountsReachEveryNode() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", k3dMountPath+"/probe.txt")
		s.Require().NoError(err, "node %s", node)
		s.Equal("from the host", strings.TrimSpace(out), "node %s", node)

		_, err = dockerClient.Exec(t.Context(), node, "touch", k3dMountPath+"/written")
		s.Error(err, "node %s must see the mount read-only", node)
	}
}

// TestExposeReachesTheAPIServerThroughSOCKS5 proves that the relay lets a
// client outside the cluster reach an in-cluster address, with a worker in
// the cluster.
func (s *K3dSuite) TestExposeReachesTheAPIServerThroughSOCKS5() {
	s.Require().Len(s.up.ExposedPorts, 1)
	ep := s.up.ExposedPorts[0]
	s.Equal("apiserver", ep.Name)

	upstream, err := url.Parse(ep.Upstream)
	s.Require().NoError(err)
	target := strings.TrimPrefix(upstream.Path, "/")
	s.Equal("kubernetes.default.svc:443", target)

	dialer, err := proxy.SOCKS5("tcp", upstream.Host, nil, proxy.Direct)
	s.Require().NoError(err)
	conn, err := dialer.Dial("tcp", target)
	s.Require().NoError(err, "the relay must reach the api server from inside the cluster")
	_ = conn.Close()
}

// TestUpReusesAnExistingClusterWithMatchingConfig proves that a second Up
// against an unchanged with block reuses the live cluster.
func (s *K3dSuite) TestUpReusesAnExistingClusterWithMatchingConfig() {
	t := s.T()
	before, err := k3dcmd.GetNodes(t.Context(), s.clusterName, nil)
	s.Require().NoError(err)
	beforeInfo, err := dockerClient.Inspect(t.Context(), before[0])
	s.Require().NoError(err)

	out := &capture{}
	_, err = Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   k3dStepName,
		Env:    s.env(),
		Config: []byte(s.configJSON()),
	}, out)
	s.Require().NoError(err)

	after, err := k3dcmd.GetNodes(t.Context(), s.clusterName, nil)
	s.Require().NoError(err)
	s.Equal(before, after)
	afterInfo, err := dockerClient.Inspect(t.Context(), after[0])
	s.Require().NoError(err)
	s.Equal(beforeInfo.ID, afterInfo.ID, "reusing the cluster must not recreate its nodes")
	s.Contains(strings.Join(out.stdout, "\n"), "reusing cluster")
}

// TestK3dOnPodman proves that the k3d driver runs the nodes on the podman
// service under the podman engine, and that Down removes them.
func TestK3dOnPodman(t *testing.T) {
	requireK3d(t)
	client := podman.Client{}
	if err := client.Available(t.Context()); err != nil {
		t.Skip("podman is unavailable:", err)
	}

	env := plugin.Env{Project: "k3d-podman-it", Workspace: t.TempDir(), Engine: "podman"}
	config := []byte(`{"driver":"k3d","coredns":false,"trust_ca":false}`)
	down := func() error {
		return Step{}.Down(context.WithoutCancel(t.Context()),
			&plugin.DownRequest{Step: k3dStepName, Env: env, Config: config}, &capture{})
	}
	t.Cleanup(func() { _ = down() })

	res, err := Step{}.Up(t.Context(), &plugin.UpRequest{Step: k3dStepName, Env: env, Config: config}, &capture{})
	if err != nil {
		t.Fatalf("Up under podman: %v", err)
	}

	nodes := strings.Split(res.Outputs["nodes"].Reveal(), ",")
	for _, node := range nodes {
		info, inspectErr := client.Inspect(t.Context(), node)
		if inspectErr != nil {
			t.Fatalf("podman does not hold node %s: %v", node, inspectErr)
		}
		if !info.Running {
			t.Errorf("node %s is not running", node)
		}
	}

	if err = down(); err != nil {
		t.Fatalf("Down under podman: %v", err)
	}
	for _, node := range nodes {
		if _, inspectErr := client.Inspect(t.Context(), node); !errors.Is(inspectErr, cri.ErrNotFound) {
			t.Errorf("node %s survived Down: %v", node, inspectErr)
		}
	}
}

func TestK3dDownIsIdempotent(t *testing.T) {
	requireDocker(t)
	requireK3d(t)

	req := &plugin.DownRequest{
		Step:   "never-created",
		Env:    plugin.Env{Project: "k3d-idempotent", Workspace: t.TempDir()},
		Config: []byte(`{"driver":"k3d"}`),
	}
	if err := (Step{}).Down(t.Context(), req, &capture{}); err != nil {
		t.Fatalf("Down of a cluster that never existed: %v", err)
	}
}
