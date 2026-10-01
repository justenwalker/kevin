//go:build integration

package kubernetes

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/proxy"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/minikubecmd"
	"github.com/justenwalker/kevin/internal/podman"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/plugin"
)

// minikubeProject names every docker and minikube resource that this suite
// creates, so the suite never collides with a developer's own cluster or
// another suite.
const minikubeProject = "minikube-it"

// minikubeDomain is the environment domain that CoreDNS forwards to the
// relay.
const minikubeDomain = "kevin.home"

// minikubeStepName is the step name that the suite passes to Up and Down.
const minikubeStepName = "cluster"

// minikubeMountPath is where the suite mounts a host directory in every node.
const minikubeMountPath = "/mnt/host"

// configJSON is the with block of the suite cluster: two workers, a relay for
// the API server, a host directory mounted read-only in every node, and the
// minikube options.
func (s *MinikubeSuite) configJSON() string {
	return fmt.Sprintf(`{"driver":"minikube","workers":{"worker":{},"worker_b":{}},"expose":{"apiserver":{"address":"kubernetes.default.svc:443"}},`+
		`"mounts":[{"host":%q,"container":%q,"readonly":true}],`+
		`"minikube":{"memory":"2g","cpus":2}}`,
		s.mountDir, minikubeMountPath)
}

// MinikubeSuite drives one minikube cluster against a real docker daemon. The
// suite creates a single cluster and asserts everything against it.
type MinikubeSuite struct {
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

func TestMinikubeSuite(t *testing.T) {
	suite.Run(t, new(MinikubeSuite))
}

// env is the plugin environment that Up and Down receive.
func (s *MinikubeSuite) env() plugin.Env {
	return plugin.Env{
		Project:   minikubeProject,
		Workspace: s.workspace,
		Network:   s.network,
		CAPath:    ca.RootCertPath(),
		Domain:    minikubeDomain,
		Relay:     s.relay.Addr(),
	}
}

// SetupSuite creates the shared network, starts the relay, generates a real
// certificate authority, and creates the cluster once for every test in the
// suite.
func (s *MinikubeSuite) SetupSuite() {
	t := s.T()
	requireDocker(t)
	requireMinikube(t)
	ensureRelayImage(t)

	s.network = "kevin-" + minikubeProject
	s.Require().NoError(dockerClient.NetworkCreate(t.Context(), s.network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: minikubeProject},
	}))

	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", minikubeProject, ca.Options{})
	_, err := m.LoadOrGenerateRoot()
	s.Require().NoError(err)
	intermediate, err := m.LoadOrGenerateIntermediate()
	s.Require().NoError(err)
	s.caPEM = intermediate.RootPEM()

	r, err := relay.Start(t.Context(), dockerClient, relay.Options{
		Project:   minikubeProject,
		Network:   s.network,
		Domain:    minikubeDomain,
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
		Step:   minikubeStepName,
		Env:    s.env(),
		Config: []byte(s.configJSON()),
	}, &capture{})
	s.Require().NoError(err, "Up must create the cluster")
	s.up = res
	s.clusterName = res.Outputs["name"].Reveal()
	s.kubeconfig = res.Outputs["kubeconfig"].Reveal()
}

// TearDownSuite removes the cluster, the relay, and the network, even when an
// earlier removal failed. It removes the state directory of minikube first,
// as a crash would leave it, so Down must find the nodes from the engine.
func (s *MinikubeSuite) TearDownSuite() {
	t := s.T()
	ctx := context.WithoutCancel(context.Background())

	if s.clusterName != "" {
		s.Require().NoError(os.RemoveAll(filepath.Join(s.workspace, "minikube")))

		downErr := Step{}.Down(t.Context(), &plugin.DownRequest{
			Step:   minikubeStepName,
			Env:    plugin.Env{Project: minikubeProject, Workspace: s.workspace},
			Config: []byte(s.configJSON()),
		}, &capture{})
		s.NoError(downErr, "Down must remove the cluster without error")
		if downErr != nil {
			_ = minikubecmd.Delete(ctx, s.clusterName, filepath.Join(s.T().TempDir(), ".minikube"), os.Stderr)
		}
		nodes, err := s.minikubeDriver().Nodes(ctx)
		s.NoError(err)
		s.Empty(nodes, "Down must remove every node, the worker included")
		_, gwErr := dockerClient.NetworkGateway(ctx, "kevin-minikube-"+s.clusterName)
		s.ErrorIs(gwErr, cri.ErrNotFound, "Down must remove the network that Create made")
	}

	if s.relay != nil {
		s.NoError(s.relay.Close())
	}
	s.NoError(dockerClient.NetworkRemove(ctx, s.network))
}

// minikubeDriver returns the driver for the suite cluster.
func (s *MinikubeSuite) minikubeDriver() *minikubeDriver {
	drv, err := newMinikubeDriver(config{}, plugin.Env{}, s.clusterName, s.kubeconfig, dockerClient)
	s.Require().NoError(err)
	return drv
}

// nodeList returns the node names Up published: the control plane first, then
// each worker.
func (s *MinikubeSuite) nodeList() []string {
	return strings.Split(s.up.Outputs["nodes"].Reveal(), ",")
}

// TestUpPublishesWhatADependentStepNeeds proves that Up returns a kubeconfig
// path that exists, the minikube context name, and the control-plane and
// worker nodes.
func (s *MinikubeSuite) TestUpPublishesWhatADependentStepNeeds() {
	_, err := os.Stat(s.kubeconfig)
	s.Require().NoError(err, "the kubeconfig path that Up publishes must exist")

	s.Equal(s.clusterName, s.up.Outputs["context"].Reveal())
	s.Equal([]string{s.clusterName, s.clusterName + "-m02", s.clusterName + "-m03"}, s.nodeList())
}

// TestExportReportsTheLiveCluster proves that Export names the same cluster
// and context that Up did, and lists its nodes.
func (s *MinikubeSuite) TestExportReportsTheLiveCluster() {
	res, err := Step{}.Export(s.T().Context(), &plugin.ExportRequest{
		Step:   minikubeStepName,
		Env:    s.env(),
		Config: []byte(s.configJSON()),
	})
	s.Require().NoError(err)

	s.Equal(s.clusterName, res.Out["name"].Reveal())
	s.Equal(s.clusterName, res.Out["context"].Reveal())
	s.Equal(s.kubeconfig, res.Out["kubeconfig"].Reveal())
	s.Len(res.Containers, len(s.nodeList()))
}

// TestAPIServerListensOnLoopbackOnly proves that the kubeconfig names a
// loopback address, and that minikube publishes no port on another
// interface.
func (s *MinikubeSuite) TestAPIServerListensOnLoopbackOnly() {
	t := s.T()
	raw, err := os.ReadFile(s.kubeconfig)
	s.Require().NoError(err)
	s.Contains(string(raw), "server: https://127.0.0.1:")

	info, err := dockerClient.Inspect(t.Context(), s.clusterName)
	s.Require().NoError(err)
	s.Contains(string(raw), "server: https://"+info.Ports[minikubeAPIPort]+"\n",
		"the kubeconfig must name the port that the engine publishes now")

	out, err := exec.CommandContext(t.Context(), "docker", "port", s.clusterName).Output()
	s.Require().NoError(err)
	s.NotEmpty(out)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		s.Contains(line, "-> 127.0.0.1:", "a published port must bind loopback, not every interface")
	}
}

// TestNodesJoinedTheSharedNetwork proves that every node joined the docker
// network of the suite, and that the network carries its default route.
func (s *MinikubeSuite) TestNodesJoinedTheSharedNetwork() {
	t := s.T()
	for _, node := range s.nodeList() {
		info, err := dockerClient.Inspect(t.Context(), node)
		s.Require().NoError(err)
		s.Contains(info.IPs, s.network)
		s.Contains(info.IPs, "kevin-minikube-"+s.clusterName)
		requireDefaultRoute(t, node, s.network)
	}
}

// TestNodesCarryTheirKevinLabel proves that the control plane is labelled
// "control-plane" and the worker by its workers key.
func (s *MinikubeSuite) TestNodesCarryTheirKevinLabel() {
	out, err := s.minikubeDriver().Kubectl(s.T().Context(), "get", "nodes", "-L", nodeLabelKey, "--no-headers")
	s.Require().NoError(err)

	lines := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		s.Require().NotEmpty(fields)
		lines[fields[0]] = fields[len(fields)-1]
	}
	s.Equal(map[string]string{
		s.clusterName:          controlPlaneNodeName,
		s.clusterName + "-m02": "worker",
		s.clusterName + "-m03": "worker_b",
	}, lines)
}

// TestCoreDNSCarriesTheForwardZone proves that Up patches CoreDNS with a
// forward zone for the domain.
func (s *MinikubeSuite) TestCoreDNSCarriesTheForwardZone() {
	out, err := s.minikubeDriver().Kubectl(s.T().Context(), "-n", "kube-system", "get", "configmap", "coredns",
		"-o", "jsonpath={.data.Corefile}")
	s.Require().NoError(err)

	s.Contains(out, minikubeDomain+":53 {")
	s.Contains(out, "forward . "+s.relay.Addr())
}

// TestNodeDNSPointsAtRelay proves that Up rewrites every node's own
// resolv.conf to name the relay as its only nameserver.
func (s *MinikubeSuite) TestNodeDNSPointsAtRelay() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", "/etc/resolv.conf")
		s.Require().NoError(err)
		s.Contains(out, "nameserver "+s.relay.Addr())
	}
}

// TestContainersReportOnePerNode proves that Up populates Containers with one
// entry per node, carrying the kubeadm pod and service subnets, and the
// control plane's Name carrying its kevin.node label.
func (s *MinikubeSuite) TestContainersReportOnePerNode() {
	s.Require().Len(s.up.Containers, len(s.nodeList()))
	for _, c := range s.up.Containers {
		s.NotEmpty(c.NetnsPath)
		s.Contains(c.ExcludeCIDRs, "10.244.0.0/16")
		s.Contains(c.ExcludeCIDRs, "10.96.0.0/12")
	}
	s.Equal(controlPlaneNodeName, s.up.Containers[0].Name)
}

// TestNodeHoldsTheKevinRoot proves that minikube installed the kevin root
// certificate in every node.
func (s *MinikubeSuite) TestNodeHoldsTheKevinRoot() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", minikubeCAPath)
		s.Require().NoError(err)
		s.Contains(normalizePEM(out), normalizePEM(s.caPEM))
	}
}

// TestOptionsReachTheCluster proves that the minikube options take effect:
// every node has the memory and CPU limits.
func (s *MinikubeSuite) TestOptionsReachTheCluster() {
	t := s.T()
	for _, node := range s.nodeList() {
		limits, err := exec.CommandContext(t.Context(), "docker", "inspect", "--format",
			"{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}}", node).Output()
		s.Require().NoError(err, "node %s", node)
		s.Equal("2147483648 2000000000", strings.TrimSpace(string(limits)), "node %s is limited to 2g and 2 cpus", node)
	}
}

// TestMountsReachEveryNode proves that the mounted host directory is visible
// in the control plane and every worker, and read-only.
func (s *MinikubeSuite) TestMountsReachEveryNode() {
	t := s.T()
	for _, node := range s.nodeList() {
		out, err := dockerClient.Exec(t.Context(), node, "cat", minikubeMountPath+"/probe.txt")
		s.Require().NoError(err, "node %s", node)
		s.Equal("from the host", strings.TrimSpace(out), "node %s", node)

		_, err = dockerClient.Exec(t.Context(), node, "touch", minikubeMountPath+"/written")
		s.Error(err, "node %s must see the mount read-only", node)
	}
}

// TestExposeReachesTheAPIServerThroughSOCKS5 proves that the relay lets a
// client outside the cluster reach an in-cluster address, with a worker in
// the cluster.
func (s *MinikubeSuite) TestExposeReachesTheAPIServerThroughSOCKS5() {
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
func (s *MinikubeSuite) TestUpReusesAnExistingClusterWithMatchingConfig() {
	t := s.T()
	before, err := s.minikubeDriver().Nodes(t.Context())
	s.Require().NoError(err)
	beforeInfo, err := dockerClient.Inspect(t.Context(), before[0])
	s.Require().NoError(err)

	out := &capture{}
	_, err = Step{}.Up(t.Context(), &plugin.UpRequest{
		Step:   minikubeStepName,
		Env:    s.env(),
		Config: []byte(s.configJSON()),
	}, out)
	s.Require().NoError(err)

	after, err := s.minikubeDriver().Nodes(t.Context())
	s.Require().NoError(err)
	s.Equal(before, after)
	afterInfo, err := dockerClient.Inspect(t.Context(), after[0])
	s.Require().NoError(err)
	s.Equal(beforeInfo.ID, afterInfo.ID, "reusing the cluster must not recreate its nodes")
	s.Contains(strings.Join(out.stdout, "\n"), "reusing cluster")
}

func requireMinikube(t *testing.T) {
	t.Helper()
	if err := minikubecmd.Available(t.Context()); err != nil {
		t.Skip("minikube is unavailable:", err)
	}
}

// TestMinikubeOnPodman proves that the minikube driver runs a cluster on
// podman under the podman engine: workers, labels, the kevin root certificate,
// a read-only mount, an exposed address, and a kubeconfig that names the
// published API port. It also proves that Down removes the nodes and the
// network that minikube names after the cluster.
func TestMinikubeOnPodman(t *testing.T) {
	requireMinikube(t)
	client := podman.Client{}
	if err := client.Available(t.Context()); err != nil {
		t.Skip("podman is unavailable:", err)
	}

	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())
	m := ca.NewManager("cwd", "", "minikube-podman-it", ca.Options{})
	_, err := m.LoadOrGenerateRoot()
	require.NoError(t, err)
	intermediate, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)

	mountDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(mountDir, "probe.txt"), []byte("from the host"), 0o600))

	network := "kevin-minikube-podman-it"
	require.NoError(t, client.NetworkCreate(t.Context(), network, cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: "minikube-podman-it"},
	}))
	t.Cleanup(func() { _ = client.NetworkRemove(context.WithoutCancel(t.Context()), network) })

	r, err := relay.Start(t.Context(), client, relay.Options{
		Project:   "minikube-podman-it",
		Network:   network,
		Domain:    minikubeDomain,
		ProxyAddr: "host.containers.internal:18080",
		Image:     relay.Ref(""),
		Authority: intermediate,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Close() })

	env := plugin.Env{
		Project: "minikube-podman-it", Workspace: t.TempDir(), Engine: "podman", CAPath: ca.RootCertPath(),
		Network: network, Domain: minikubeDomain, Relay: r.Addr(),
	}
	with := []byte(fmt.Sprintf(`{"driver":"minikube","workers":{"worker":{}},`+
		`"expose":{"apiserver":{"address":"kubernetes.default.svc:443"}},`+
		`"mounts":[{"host":%q,"container":%q,"readonly":true}]}`, mountDir, minikubeMountPath))
	down := func() error {
		return Step{}.Down(context.WithoutCancel(t.Context()),
			&plugin.DownRequest{Step: minikubeStepName, Env: env, Config: with}, &capture{})
	}
	t.Cleanup(func() { _ = down() })

	res, err := Step{}.Up(t.Context(), &plugin.UpRequest{Step: minikubeStepName, Env: env, Config: with}, &capture{})
	require.NoError(t, err, "Up under podman")

	name := res.Outputs["name"].Reveal()
	kubeconfig := res.Outputs["kubeconfig"].Reveal()
	nodes := strings.Split(res.Outputs["nodes"].Reveal(), ",")
	require.Equal(t, []string{name, name + "-m02"}, nodes, "the control plane and one worker")

	for _, node := range nodes {
		info, inspectErr := client.Inspect(t.Context(), node)
		require.NoError(t, inspectErr, "podman holds node %s", node)
		assert.True(t, info.Running, "node %s is running", node)

		cert, execErr := client.Exec(t.Context(), node, "cat", minikubeCAPath)
		require.NoError(t, execErr, "node %s", node)
		assert.Contains(t, normalizePEM(cert), normalizePEM(intermediate.RootPEM()), "node %s trusts the kevin root", node)

		probe, execErr := client.Exec(t.Context(), node, "cat", minikubeMountPath+"/probe.txt")
		require.NoError(t, execErr, "node %s", node)
		assert.Equal(t, "from the host", strings.TrimSpace(probe), "node %s", node)
		_, execErr = client.Exec(t.Context(), node, "touch", minikubeMountPath+"/written")
		require.Error(t, execErr, "node %s sees the mount read-only", node)
	}

	drv, err := newMinikubeDriver(config{}, plugin.Env{Engine: "podman"}, name, kubeconfig, client)
	require.NoError(t, err)

	labels, err := drv.Kubectl(t.Context(), "get", "nodes", "-L", nodeLabelKey, "--no-headers")
	require.NoError(t, err)
	assert.Contains(t, labels, controlPlaneNodeName)
	assert.Contains(t, labels, "worker")

	raw, err := os.ReadFile(kubeconfig)
	require.NoError(t, err)
	info, err := client.Inspect(t.Context(), name)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "server: https://"+info.Ports[minikubeAPIPort]+"\n",
		"the kubeconfig names the port that podman publishes now")

	require.Len(t, res.ExposedPorts, 1)
	upstream, err := url.Parse(res.ExposedPorts[0].Upstream)
	require.NoError(t, err)
	dialer, err := proxy.SOCKS5("tcp", upstream.Host, nil, proxy.Direct)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		conn, dialErr := dialer.Dial("tcp", strings.TrimPrefix(upstream.Path, "/"))
		if dialErr != nil {
			t.Log("dial through the relay:", dialErr)
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second, time.Second, "the relay reaches the api server from inside the cluster")

	require.NoError(t, down(), "Down under podman")
	for _, node := range nodes {
		_, inspectErr := client.Inspect(t.Context(), node)
		require.ErrorIs(t, inspectErr, cri.ErrNotFound, "node %s survived Down", node)
	}
	left, err := client.ListByLabel(t.Context(), minikubeNodeLabel, "true")
	require.NoError(t, err)
	for _, node := range nodes {
		assert.NotContains(t, left, node, "minikube containers survived Down")
	}
}

func TestMinikubeDownIsIdempotent(t *testing.T) {
	requireDocker(t)
	requireMinikube(t)

	req := &plugin.DownRequest{
		Step:   "never-created",
		Env:    plugin.Env{Project: "minikube-idempotent", Workspace: t.TempDir()},
		Config: []byte(`{"driver":"minikube"}`),
	}
	if err := (Step{}).Down(t.Context(), req, &capture{}); err != nil {
		t.Fatalf("Down of a cluster that never existed: %v", err)
	}
}

// TestMinikubeRebuildsOnConfigChange proves that an Up whose with block
// changed replaces the control plane and the worker instead of reusing them,
// and that the new cluster answers through the kubeconfig.
func TestMinikubeRebuildsOnConfigChange(t *testing.T) {
	requireDocker(t)
	requireMinikube(t)

	env := plugin.Env{Project: "minikube-rebuild-it", Workspace: t.TempDir()}
	withMemory := func(memory string) []byte {
		return []byte(fmt.Sprintf(`{"driver":"minikube","workers":{"worker":{}},"coredns":false,"trust_ca":false,`+
			`"minikube":{"memory":%q}}`, memory))
	}
	down := func() error {
		return Step{}.Down(context.WithoutCancel(t.Context()),
			&plugin.DownRequest{Step: minikubeStepName, Env: env, Config: withMemory("2g")}, &capture{})
	}
	t.Cleanup(func() { _ = down() })

	up := func(memory string) (*plugin.Result, *capture) {
		out := &capture{}
		res, err := Step{}.Up(t.Context(), &plugin.UpRequest{Step: minikubeStepName, Env: env, Config: withMemory(memory)}, out)
		require.NoError(t, err, "Up with memory %s", memory)
		return res, out
	}
	limit := func(node string) string {
		out, err := exec.CommandContext(t.Context(), "docker", "inspect", "--format", "{{.HostConfig.Memory}}", node).Output()
		require.NoError(t, err, "node %s", node)
		return strings.TrimSpace(string(out))
	}

	first, _ := up("2g")
	nodes := strings.Split(first.Outputs["nodes"].Reveal(), ",")
	require.Len(t, nodes, 2)
	ids := map[string]string{}
	for _, node := range nodes {
		info, err := dockerClient.Inspect(t.Context(), node)
		require.NoError(t, err)
		ids[node] = info.ID
		require.Equal(t, "2147483648", limit(node), "node %s", node)
	}

	second, out := up("3g")

	assert.NotContains(t, strings.Join(out.stdout, "\n"), "reusing cluster", "a changed with block must not reuse the cluster")
	require.Equal(t, nodes, strings.Split(second.Outputs["nodes"].Reveal(), ","))
	for _, node := range nodes {
		info, err := dockerClient.Inspect(t.Context(), node)
		require.NoError(t, err)
		assert.NotEqual(t, ids[node], info.ID, "node %s must be recreated", node)
		assert.Equal(t, "3221225472", limit(node), "node %s has the new limit", node)
	}

	drv, err := newMinikubeDriver(config{}, plugin.Env{}, second.Outputs["name"].Reveal(), second.Outputs["kubeconfig"].Reveal(), dockerClient)
	require.NoError(t, err)
	got, err := drv.Kubectl(t.Context(), "get", "nodes", "-o", "name")
	require.NoError(t, err)
	assert.Len(t, strings.Fields(got), 2, "both nodes joined the rebuilt cluster")

	raw, err := os.ReadFile(second.Outputs["kubeconfig"].Reveal())
	require.NoError(t, err)
	info, err := dockerClient.Inspect(t.Context(), nodes[0])
	require.NoError(t, err)
	assert.Contains(t, string(raw), "server: https://"+info.Ports[minikubeAPIPort]+"\n")
}

// TestMinikubeRetainKeepsAFailedCluster proves that a start that fails after
// the nodes exist removes them, unless retain is set. A wait too short for
// the control plane to become ready makes the start fail.
func TestMinikubeRetainKeepsAFailedCluster(t *testing.T) {
	requireDocker(t)
	requireMinikube(t)

	for _, tc := range []struct {
		name   string
		retain bool
	}{
		{name: "retain keeps the nodes", retain: true},
		{name: "no retain removes the nodes", retain: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := plugin.Env{Project: fmt.Sprintf("minikube-retain-%t-it", tc.retain), Workspace: t.TempDir()}
			with := []byte(fmt.Sprintf(`{"driver":"minikube","wait":"10s","retain":%t,"coredns":false,"trust_ca":false}`, tc.retain))
			t.Cleanup(func() {
				_ = Step{}.Down(context.WithoutCancel(t.Context()),
					&plugin.DownRequest{Step: minikubeStepName, Env: env, Config: with}, &capture{})
			})

			_, err := Step{}.Up(t.Context(), &plugin.UpRequest{Step: minikubeStepName, Env: env, Config: with}, &capture{})
			require.Error(t, err, "a 10s wait must fail the start")

			drv, err := newMinikubeDriver(config{}, plugin.Env{}, env.Project+"-"+minikubeStepName, "", dockerClient)
			require.NoError(t, err)
			nodes, err := drv.Nodes(t.Context())
			require.NoError(t, err)
			if tc.retain {
				assert.Equal(t, []string{drv.name}, nodes, "retain leaves the control plane for inspection")
			} else {
				assert.Empty(t, nodes, "a failed start removes its nodes")
			}
		})
	}
}

// TestMinikubeConcurrentClusters proves that two clusters starting at once
// share the minikube cache without corrupting it: both come up, and both
// answer. The Kubernetes version is one the cache may not hold yet, so the
// starts compete to download it.
func TestMinikubeConcurrentClusters(t *testing.T) {
	requireDocker(t)
	requireMinikube(t)

	type cluster struct {
		env  plugin.Env
		with []byte
		res  *plugin.Result
		err  error
	}
	clusters := make([]*cluster, 2)
	for i := range clusters {
		clusters[i] = &cluster{
			env: plugin.Env{Project: fmt.Sprintf("minikube-concurrent-%d-it", i), Workspace: t.TempDir()},
			with: []byte(`{"driver":"minikube","coredns":false,"trust_ca":false,` +
				`"minikube":{"kubernetes_version":"v1.30.0"}}`),
		}
		t.Cleanup(func() {
			_ = Step{}.Down(context.WithoutCancel(t.Context()),
				&plugin.DownRequest{Step: minikubeStepName, Env: clusters[i].env, Config: clusters[i].with}, &capture{})
		})
	}

	var wg sync.WaitGroup
	for _, c := range clusters {
		wg.Go(func() {
			c.res, c.err = Step{}.Up(t.Context(), &plugin.UpRequest{Step: minikubeStepName, Env: c.env, Config: c.with}, &capture{})
		})
	}
	wg.Wait()

	for i, c := range clusters {
		require.NoError(t, c.err, "cluster %d", i)
		drv, err := newMinikubeDriver(config{}, plugin.Env{}, c.res.Outputs["name"].Reveal(), c.res.Outputs["kubeconfig"].Reveal(), dockerClient)
		require.NoError(t, err)
		out, err := drv.Kubectl(t.Context(), "get", "nodes", "-o", "name")
		require.NoError(t, err, "cluster %d", i)
		assert.Len(t, strings.Fields(out), 1, "cluster %d", i)
	}
}
