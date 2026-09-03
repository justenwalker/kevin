//go:build integration

package kind

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
	"golang.org/x/net/proxy"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/internal/plugins/route"
	"github.com/justenwalker/kevin/internal/plugins/wait"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/plugin"
)

// kindProject names every docker and kind resource that this suite creates.
// The name stays unique, so the suite never collides with a developer's own
// cluster or with another suite.
const kindProject = "kevin-it-kind"

// kindDomain is the environment domain that CoreDNS forwards to the relay.
const kindDomain = "kevin.home"

// kindStepName is the step name that the suite passes to Up and Down.
const kindStepName = "cluster"

// kindRelayImageTag matches RelayImageTag in build/main.go.
const kindRelayImageTag = "kevin-relay:dev"

// KindSuite drives one kind cluster against a real docker daemon. A cluster
// takes minutes to create, so the suite creates exactly one and asserts
// everything against it.
type KindSuite struct {
	suite.Suite

	network     string
	workspace   string
	caPEM       string
	relay       *relay.Relay
	clusterName string
	up          *plugin.Result
}

func TestKindSuite(t *testing.T) {
	suite.Run(t, new(KindSuite))
}

// SetupSuite creates the shared network, starts the relay, generates a real
// certificate authority, and creates the cluster once for every test in the
// suite.
func (s *KindSuite) SetupSuite() {
	t := s.T()
	requireDocker(t)
	ensureRelayImage(t)

	s.network = "kevin-" + kindProject
	s.Require().NoError(dockerClient.NetworkCreate(t.Context(), s.network, map[string]string{
		cri.LabelProject: kindProject,
	}))

	r, err := relay.Start(t.Context(), relay.Options{
		Project:   kindProject,
		Network:   s.network,
		Domain:    kindDomain,
		ProxyAddr: "host.docker.internal:18080",
		Image:     relay.Ref(""),
	})
	s.Require().NoError(err)
	s.relay = r

	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", kindProject, ca.Options{})
	_, err = m.LoadOrGenerateRoot()
	s.Require().NoError(err)
	intermediate, err := m.LoadOrGenerateIntermediate()
	s.Require().NoError(err)
	s.caPEM = intermediate.RootPEM()

	requireKind(t)
	s.workspace = t.TempDir()

	res, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: kindStepName,
		Env: plugin.Env{
			Project:   kindProject,
			Workspace: s.workspace,
			Network:   s.network,
			CAPath:    ca.RootCertPath(),
			Domain:    kindDomain,
			Relay:     s.relay.Addr(),
		},
		Config: []byte(`{"workers":0,"expose":[{"address":"kubernetes.default.svc:443","name":"apiserver"}]}`),
	}, &capture{})
	s.Require().NoError(err, "Up must create the cluster")
	s.up = res
	s.clusterName = res.Outputs["name"].Reveal()
}

// TearDownSuite removes the cluster, the relay, and the network, even when an
// earlier removal failed.
func (s *KindSuite) TearDownSuite() {
	t := s.T()
	ctx := context.WithoutCancel(context.Background())

	downErr := Step{}.Down(t.Context(), &plugin.DownRequest{
		Step:   kindStepName,
		Env:    plugin.Env{Project: kindProject, Workspace: s.workspace},
		Config: []byte(`{"workers":0}`),
	}, &capture{})
	s.NoError(downErr, "Down must remove the cluster without error")
	if downErr != nil {
		// Down failed. A leaked cluster holds gigabytes, so remove it
		// directly as a last resort.
		_ = kindcmd.Delete(ctx, kindcmd.DeleteSpec{Name: s.clusterName}, io.Discard)
	}

	if list, err := kindcmd.GetClusters(ctx); s.NoError(err) {
		s.NotContains(list, s.clusterName,
			"the cluster must not appear in the kind cluster list after Down")
	}

	if s.relay != nil {
		s.NoError(s.relay.Close())
	}
	s.NoError(dockerClient.NetworkRemove(ctx, s.network))
}

// controlPlaneNode returns the container name of the control plane node of
// the suite cluster.
func (s *KindSuite) controlPlaneNode() string {
	allNodes, err := kindcmd.GetNodes(s.T().Context(), s.clusterName)
	s.Require().NoError(err)
	node, err := bootstrapControlPlaneNode(allNodes)
	s.Require().NoError(err)
	return node
}

// TestUpPublishesWhatADependentStepNeeds proves that Up returns a kubeconfig
// path that exists, a context name, and the node names.
func (s *KindSuite) TestUpPublishesWhatADependentStepNeeds() {
	kubeconfig := s.up.Outputs["kubeconfig"].Reveal()
	s.NotEmpty(kubeconfig, "a dependent step reads the kubeconfig path that Up publishes")
	_, err := os.Stat(kubeconfig)
	s.Require().NoError(err, "the kubeconfig path that Up publishes must exist")

	s.Equal("kind-"+s.clusterName, s.up.Outputs["context"].Reveal(), "a dependent step reads the context that Up publishes")

	nodeList := strings.Split(s.up.Outputs["nodes"].Reveal(), ",")
	s.NotEmpty(nodeList, "a dependent step reads the node names that Up publishes")
}

// TestNodesJoinedTheSharedNetwork proves that a node of the cluster joined
// the docker network of the suite, on top of the network of kind.
func (s *KindSuite) TestNodesJoinedTheSharedNetwork() {
	t := s.T()
	nodeList := strings.Split(s.up.Outputs["nodes"].Reveal(), ",")
	info, err := dockerClient.Inspect(t.Context(), nodeList[0])
	s.Require().NoError(err)
	s.Contains(info.IPs, s.network, "a pod reaches a container step only when the node joins the shared network")
}

// TestCoreDNSCarriesTheForwardZone proves that Up patches CoreDNS with a
// forward zone for the domain, and that the original zone survives.
func (s *KindSuite) TestCoreDNSCarriesTheForwardZone() {
	t := s.T()
	container := s.controlPlaneNode()

	out, err := kubectl(t.Context(), container, "-n", "kube-system", "get", "configmap", "coredns",
		"-o", "jsonpath={.data.Corefile}")
	s.Require().NoError(err)

	s.Contains(out, kindDomain+":53 {", "a pod reaches a step only when CoreDNS forwards the zone")
	s.Contains(out, "forward . "+s.relay.Addr(), "the forward zone must point at the relay address")
	s.Contains(out, ".:53 {", "the patch must not remove the original block")
}

// TestNodeTrustsTheKevinRoot proves that Up installs the kevin root
// certificate into the control plane node.
func (s *KindSuite) TestNodeTrustsTheKevinRoot() {
	t := s.T()
	container := s.controlPlaneNode()

	out, err := dockerClient.Exec(t.Context(), container, "cat", caAnchorPath)
	s.Require().NoError(err)
	s.Equal(s.caPEM, out, "a pull through the proxy verifies only when the node trusts the kevin root")
}

// TestContainerdAnswersAfterTheRestart proves that containerd answers on the
// control plane node once Up returns. installTrustCA restarts containerd,
// and waitContainerdReady waits for it during Up.
func (s *KindSuite) TestContainerdAnswersAfterTheRestart() {
	t := s.T()
	container := s.controlPlaneNode()

	_, err := dockerClient.Exec(t.Context(), container, "ctr", "version")
	s.Require().NoError(err, "a regression in waitContainerdReady must show here, not as a pull failure later")
}

// TestExposeReachesTheAPIServerThroughSOCKS5 proves that Up's SOCKS5 relay
// actually lets a client outside the cluster reach an in-cluster address.
// The target is the kube-apiserver's own ClusterIP service, which always
// exists, so the test needs no extra in-cluster workload of its own.
func (s *KindSuite) TestExposeReachesTheAPIServerThroughSOCKS5() {
	s.Require().Len(s.up.ExposedPorts, 1)
	ep := s.up.ExposedPorts[0]
	s.Equal("apiserver", ep.Name)
	s.Equal("socks5", ep.Protocol)

	relayAddr, target, ok := strings.Cut(strings.TrimPrefix(ep.Upstream, "socks5://"), "/")
	s.Require().True(ok, "Upstream must carry both the relay address and the target")
	s.Equal("kubernetes.default.svc:443", target)

	dialer, err := proxy.SOCKS5("tcp", relayAddr, nil, proxy.Direct)
	s.Require().NoError(err)

	conn, err := dialer.Dial("tcp", target)
	s.Require().NoError(err, "the relay must reach the api server from inside the cluster")
	_ = conn.Close()
}

// TestRouteBuildsTheSameUpstreamShapeExposeAlreadyProvesReachable proves
// builtin:route's Route.Upstream, built from the real relay_addr this
// cluster published, is the exact same socks5://<relay>/<target> shape
// TestExposeReachesTheAPIServerThroughSOCKS5 already dials successfully
// against this same live cluster - so a real SOCKS5 CONNECT through it
// really does complete a TLS handshake against the real API server.
// internal/proxy/relay_test.go separately proves, against a local SOCKS5
// server, that the proxy's own dial-through-relay mechanism correctly
// delivers a full HTTP request end to end; this test is what ties that
// mechanism's input shape to a real cluster instead of a local double.
func (s *KindSuite) TestRouteBuildsTheSameUpstreamShapeExposeAlreadyProvesReachable() {
	t := s.T()
	relayAddr, ok := s.up.Outputs["relay_addr"]
	s.Require().True(ok, "Up must publish relay_addr when expose is non-empty")

	result, err := route.Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: "apiserver_route",
		Env:  plugin.Env{Domain: kindDomain},
		Config: []byte(fmt.Sprintf(
			`{"relay":%q,"routes":[{"host":"apiserver","address":"kubernetes.default.svc:443","tls":true}]}`,
			relayAddr,
		)),
	}, &capture{})
	s.Require().NoError(err)
	s.Require().Len(result.Routes, 1)

	r := result.Routes[0]
	s.Equal("apiserver.kevin.home", r.Host)
	s.True(r.TLS)

	relay, target, ok := strings.Cut(strings.TrimPrefix(r.Upstream, "socks5://"), "/")
	s.Require().True(ok, "Upstream must carry both the relay address and the target")
	s.Equal(relayAddr.Reveal(), relay)
	s.Equal("kubernetes.default.svc:443", target)

	// The dial and handshake themselves: identical to
	// TestExposeReachesTheAPIServerThroughSOCKS5's dial, plus wrapping the
	// connection in TLS to prove the target on the other end of the
	// tunnel really does speak TLS. InsecureSkipVerify is fine here - the
	// test verifies reachability through the relay, not the cluster's
	// certificate trust chain (kevin's proxy would itself need to trust
	// the cluster's own CA to route to it with a verified certificate,
	// which is a separate, cluster-specific concern from this feature).
	dialer, err := proxy.SOCKS5("tcp", relay, nil, proxy.Direct)
	s.Require().NoError(err)
	conn, err := dialer.Dial("tcp", target)
	s.Require().NoError(err, "the relay must reach the api server from inside the cluster")
	defer conn.Close() //nolint:errcheck // best effort, the test is done with it either way

	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	s.Require().NoError(tlsConn.HandshakeContext(t.Context()), "the target must speak TLS on the routed port")
}

// TestWaitTCPReachesTheAPIServerThroughTheRelay proves builtin:wait's tcp
// check, not just a raw SOCKS5 dial, succeeds against the same target
// TestExposeReachesTheAPIServerThroughSOCKS5 already reaches directly.
func (s *KindSuite) TestWaitTCPReachesTheAPIServerThroughTheRelay() {
	address := s.up.ExposedPorts[0].Upstream
	s.Require().NotEmpty(address)

	_, err := wait.Step{}.Up(s.T().Context(), &plugin.UpRequest{
		Config: json.RawMessage(fmt.Sprintf(`{"timeout":"10s","interval":"200ms","tcp":{"address":%q}}`, address)),
	}, &capture{})
	s.Require().NoError(err)
}

// TestUpReusesAnExistingClusterWithMatchingConfig proves a second Up against
// an unchanged with block reuses the live cluster in place instead of
// deleting and recreating it - the whole point of a persistent setup-scope
// cluster is that it survives a second "kevin setup".
func (s *KindSuite) TestUpReusesAnExistingClusterWithMatchingConfig() {
	t := s.T()

	before, err := kindcmd.GetNodes(t.Context(), s.clusterName)
	s.Require().NoError(err)

	out := &capture{}
	res, err := Step{}.Up(t.Context(), &plugin.UpRequest{
		Step: kindStepName,
		Env: plugin.Env{
			Project:   kindProject,
			Workspace: s.workspace,
			Network:   s.network,
			CAPath:    ca.RootCertPath(),
			Domain:    kindDomain,
			Relay:     s.relay.Addr(),
		},
		Config: []byte(`{"workers":0,"expose":[{"address":"kubernetes.default.svc:443","name":"apiserver"}]}`),
	}, out)
	s.Require().NoError(err)

	after, err := kindcmd.GetNodes(t.Context(), s.clusterName)
	s.Require().NoError(err)
	s.ElementsMatch(before, after, "reusing the cluster must not destroy and recreate its nodes")
	s.Equal(res.Outputs["name"].Reveal(), s.clusterName)
	s.Contains(strings.Join(out.stdout, "\n"), "reusing cluster")
}

// TestUpIsIdempotent proves that a second patch of CoreDNS and a second
// install of the trust certificate succeed against the running cluster.
//
// reuseOrCreateCluster only skips the delete-then-create - it still runs
// finishClusterSetup (CoreDNS patch, trust CA install) every time, so those
// two must tolerate running twice against a cluster that already has them
// applied. This calls them directly rather than through a second Up, to
// isolate the check from TestUpReusesAnExistingClusterWithMatchingConfig's
// own assertions.
func (s *KindSuite) TestUpIsIdempotent() {
	t := s.T()
	ctx := t.Context()

	allNodes, err := kindcmd.GetNodes(ctx, s.clusterName)
	s.Require().NoError(err)

	s.Require().NoError(patchCoreDNS(ctx, allNodes, kindDomain, s.relay.Addr(), &capture{}),
		"a second patch must not fail")
	s.Require().NoError(installTrustCA(ctx, allNodes, s.caPEM, &capture{}),
		"a second install must not fail")

	container := s.controlPlaneNode()
	out, err := kubectl(ctx, container, "-n", "kube-system", "get", "configmap", "coredns",
		"-o", "jsonpath={.data.Corefile}")
	s.Require().NoError(err)
	s.Equal(1, strings.Count(out, kindDomain+":53 {"), "a second patch must replace the zone, not add a second one")
}

// ensureRelayImage builds the relay image from source when it is absent, the
// same way as the relay-image build target. It skips the suite when it
// cannot build the image.
func ensureRelayImage(t *testing.T) {
	t.Helper()

	check := exec.CommandContext(t.Context(), "docker", "image", "inspect", kindRelayImageTag)
	if check.Run() == nil {
		return
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate the repository root to build the relay image")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")

	dir := t.TempDir()
	bin := filepath.Join(dir, "kevin-relay")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/kevin-relay")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skip("cannot build kevin-relay for the image:", err, string(out))
	}

	dockerBuild := exec.CommandContext(t.Context(), "docker", "build",
		"-f", filepath.Join(root, "build", "relay.Dockerfile"), "-t", kindRelayImageTag, dir)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		t.Skip("cannot build the relay image:", err, string(out))
	}
}
