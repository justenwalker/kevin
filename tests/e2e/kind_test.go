//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// kindTimeout is generous: kind pulls node images on a cold cache, and a
// full cluster + Deployment + Helm chart bring-up is the slowest thing in
// this whole suite.
const kindTimeout = 8 * defaultTimeout

// kindCUE mirrors examples/kind/kevin.cue: a registry, a kind cluster whose
// nodes join kevin's shared network in place of kind's own default network, a
// kubectl-applied Deployment and a Helm chart each gated by their own
// builtin:wait check, and a relay-routed Service reachable through the
// proxy. chartDir is the absolute path to examples/kind/charts/hello - the
// example's own chart, reused rather than duplicated.
const kindCUE = `project: "%s"

env: {
	registry: {
		uses:  "builtin:container"
		label: "Local Registry"
		with: {
			image:  "registry:3"
			expose: registry: {port: 5000}
		}
	}
	registry_ready: {
		uses:  "builtin:wait"
		label: "Registry Ready"
		needs: ["registry"]
		with: {
			timeout: "10s"
			http: url: "http://${needs.registry.out.host_5000}/v2/"
		}
	}
	cluster: {
		uses:  "builtin:kubernetes"
		label: "Kind Cluster"
		needs: ["registry"]
		with: {
			driver: "kind"
			workers: worker: {}
			wait:    "5m"
			egress:  ["docker.io", "*.docker.io", "*.docker.com"]
			expose: apiserver: address: "kubernetes.default.svc:443"
		}
	}
	apiserver_ready: {
		uses:  "builtin:wait"
		label: "API Server Ready"
		needs: ["cluster"]
		with: {
			timeout: "30s"
			tcp: address: "${needs.cluster.system.expose_apiserver}"
		}
	}
	app: {
		uses:  "builtin:kubectl"
		label: "App Deployment"
		needs: ["cluster"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			manifest:   "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: app\nspec:\n  replicas: 1\n  selector:\n    matchLabels: {app: app}\n  template:\n    metadata:\n      labels: {app: app}\n    spec:\n      containers:\n      - name: app\n        image: nginx:alpine\n---\napiVersion: v1\nkind: Service\nmetadata:\n  name: app\nspec:\n  selector: {app: app}\n  ports:\n  - port: 80\n"
		}
	}
	app_ready: {
		uses:  "builtin:wait"
		label: "App Ready"
		needs: ["cluster", "app"]
		with: {
			timeout: "2m"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "deployment/app"
				rollout:    true
			}
		}
	}
	chart: {
		uses:  "builtin:helm"
		label: "Hello Chart"
		needs: ["cluster"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			release:    "hello"
			chart:      %s
			wait:       ""
		}
	}
	chart_ready: {
		uses:  "builtin:wait"
		label: "Chart Ready"
		needs: ["cluster", "chart"]
		with: {
			timeout: "2m"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "deployment/hello"
				for:        "condition=Available"
			}
		}
	}
	app_route: {
		uses:  "builtin:route"
		label: "App Route"
		needs: ["cluster", "app_ready"]
		with: {
			relay: "${needs.cluster.out.relay_addr}"
			routes: [{host: "app", address: "app.default.svc.cluster.local:80"}]
		}
	}
	capture_probe: {
		uses:  "builtin:kubectl"
		label: "Capture Probe"
		needs: ["cluster", "app_ready"]
		with: {
			kubeconfig: "${needs.cluster.out.kubeconfig}"
			context:    "${needs.cluster.out.context}"
			manifest: """
				apiVersion: v1
				kind: Pod
				metadata:
				  name: capture-probe
				spec:
				  restartPolicy: Never
				  containers:
				  - name: probe
				    image: curlimages/curl
				    command: ["sh", "-c"]
				    args:
				    - |
				      echo in-cluster:; curl -sk --max-time 5 https://kubernetes.default.svc.cluster.local/
				      echo external:; curl -sk --max-time 5 https://example.com/
				"""
		}
	}
	capture_probe_done: {
		uses:  "builtin:wait"
		label: "Capture Probe Done"
		needs: ["cluster", "capture_probe"]
		with: {
			timeout: "30s"
			kubectl: {
				kubeconfig: "${needs.cluster.out.kubeconfig}"
				context:    "${needs.cluster.out.context}"
				resource:   "pod/capture-probe"
				for:        "jsonpath={.status.phase}=Succeeded"
			}
		}
	}
}

commands: {
	nodes: {
		needs: ["cluster"]
		run: ["kubectl", "--kubeconfig", "${needs.cluster.out.kubeconfig}", "get", "nodes"]
	}
	pods: {
		needs: ["cluster"]
		run: ["kubectl", "--kubeconfig", "${needs.cluster.out.kubeconfig}", "get", "pods", "-A"]
	}
}
`

// KindSuite covers a user running a kind cluster (builtin:kubernetes,
// builtin:kubectl, builtin:helm, relay routing) and kevin do against it.
// SetupSuite brings up one cluster - by far the most expensive part of the
// whole e2e run - and TearDownSuite tears it down once; sharing it between
// both avoids paying kind's slow bring-up twice.
//
// Tier: e2e.
type KindSuite struct {
	e2eSuite

	dir string
	p   *kevinProc
	out string
}

func TestKindSuite(t *testing.T) {
	suite.Run(t, new(KindSuite))
}

func (s *KindSuite) SetupSuite() {
	s.requireDocker()

	const project = "kevin-e2e-kind"
	chartDir := filepath.Join(repoRoot(), "examples", "kind", "charts", "hello")
	src := fmt.Sprintf(kindCUE, project, strconv.Quote(chartDir))
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+src)
	s.cleanupProject(project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("app_route", "ready"), kindTimeout)
	s.waitFor(s.p, stepLine("chart_ready", "ready"), kindTimeout)
	s.waitFor(s.p, stepLine("capture_probe_done", "ready"), kindTimeout)
	s.out = s.p.buf.String()
}

func (s *KindSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	require := s.Require()
	require.NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.Require().Equal(0, s.waitExit(s.p, kindTimeout), "output:\n%s", s.p.buf.String())

	s.Empty(s.containerIDsForProject("kevin-e2e-kind"), "Ctrl-C must remove the cluster's containers")
	s.requireNoKindCluster("kevin-e2e-kind")
}

// TestClusterAndDeploymentsReady confirms every step in the cluster's own
// DAG reached ready: the registry, the cluster, both readiness gates, the
// kubectl Deployment, and the Helm chart.
func (s *KindSuite) TestClusterAndDeploymentsReady() {
	for _, step := range []string{
		"registry", "registry_ready", "cluster", "apiserver_ready",
		"app", "app_ready", "chart", "chart_ready", "app_route",
		"capture_probe", "capture_probe_done",
	} {
		s.Contains(s.out, stepLine(step, "ready"), "%s must reach ready", step)
	}
}

// TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic proves the
// node-level capture docs/site/content/docs/concepts/relay.md's "Transparent
// capture" section describes: capture_probe (SetupSuite) is a Pod that
// dials the real kubernetes.default Service and the real example.com, with
// no route registered for either - so any interception here can only come
// from node-level capture, not from the relay's DNS-based intercept
// mechanism. The Service request must reach the real API server (its
// response body never carries kevin's own deny-page marker, proving the
// pod/service CIDR exclusion holds); the external request must land on
// kevin's proxy instead of the real internet (its response body does carry
// that marker, proving the redirect itself fired).
func (s *KindSuite) TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", "kevin-e2e-kind-cluster")

	out, err := exec.CommandContext(s.T().Context(), "kubectl",
		"--kubeconfig", kubeconfig, "logs", "pod/capture-probe").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)

	inCluster, external, ok := strings.Cut(string(out), "external:\n")
	s.Require().True(ok, "capture-probe log must carry both probes, got:\n%s", out)

	const denyMarker = "kevin blocked a request to"
	s.NotContains(inCluster, denyMarker, "a pod's own request to a cluster Service must reach it directly, not the proxy's deny page")
	s.Contains(external, denyMarker, "a pod's request to an unregistered external host on a captured port must land on the proxy, not the real internet")
}

// TestKubectlGetNodesThroughWrittenKubeconfig covers the doc's own
// "KUBECONFIG=... kubectl get nodes" check.
func (s *KindSuite) TestKubectlGetNodesThroughWrittenKubeconfig() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", "kevin-e2e-kind-cluster")
	out, err := exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", kubeconfig, "get", "nodes").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)
	s.Contains(string(out), "Ready")
}

// TestAppRouteReachesTheServiceThroughTheRelay covers the relay-routed
// HTTPS route: app.kevin.home reaches the nginx Service through the
// cluster's own SOCKS5 relay.
func (s *KindSuite) TestAppRouteReachesTheServiceThroughTheRelay() {
	var proxyAddr string
	for _, row := range addrRE.FindAllStringSubmatch(s.out, -1) {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr)

	// kube-proxy can lag a few seconds behind the Deployment's own rollout
	// status syncing the Service's iptables rules, so a 502 right after
	// app_ready is a transient readiness gap, not a routing bug - retry
	// briefly before failing.
	var body string
	s.Require().Eventually(func() bool {
		body = s.fetchThroughProxyOnce(proxyAddr, "https://app.kevin.home/")
		return strings.Contains(body, "Welcome to nginx")
	}, 30*time.Second, time.Second, "must reach the nginx pod through the relay-routed Service, last body:\n%s", body)
}

// TestDoExecsCommandWithExportedOutput covers "kevin do <name>": it renders
// a commands: entry's run against the needed step's Export output and execs
// it - see the "pods" entry kindCUE declares below app_route.
func (s *KindSuite) TestDoExecsCommandWithExportedOutput() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	out, code := s.runToCompletion(s.dir, "-C", s.dir, "do", "pods")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "kube-system")
}

// keepCUE puts the cluster in the setup scope (kevin run's teardown never
// touches it) and a kubectl step with keep: true in the env scope, so
// "kevin run"'s own SIGINT teardown - which only ever touches the env
// scope - is the thing under test: keeper's Down either deletes the
// manifest or, because of keep, leaves it, and the still-live setup
// cluster is what makes that observable afterward with no race against
// the cluster's own removal.
const keepCUE = `project: "%s"

setup: cluster: {
	uses:  "builtin:kubernetes"
	label: "Kind Cluster"
	with: {
		driver: "kind"
		workers: {}
		wait:    "5m"
	}
}
env: keeper: {
	uses:  "builtin:kubectl"
	label: "Keeper"
	needs: ["setup.cluster"]
	with: {
		kubeconfig: "${setup.cluster.out.kubeconfig}"
		context:    "${setup.cluster.out.context}"
		manifest:   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: keepme\ndata: {x: \"1\"}\n"
		keep:       true
	}
}
`

// KindKeepSuite covers a user keeping a kubectl or helm resource across a
// run with the keep: field - its own suite, and its own setup-scope cluster, because
// proving keep needs a real "kevin run" teardown to happen (KindSuite's
// shared cluster never runs one mid-suite).
//
// Tier: e2e.
type KindKeepSuite struct {
	e2eSuite

	dir     string
	project string
}

func TestKindKeepSuite(t *testing.T) {
	suite.Run(t, new(KindKeepSuite))
}

func (s *KindKeepSuite) SetupSuite() {
	s.requireDocker()
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}

	s.project = "kevin-e2e-kind-keep"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(keepCUE, s.project))
	s.cleanupProject(s.project)

	out, code := s.runToCompletion(s.dir, "-C", s.dir, "setup")
	s.Require().Equal(0, code, "kevin setup output:\n%s", out)
	s.Require().Contains(out, stepLine("cluster", "ready"))
}

func (s *KindKeepSuite) TearDownSuite() {
	if s.dir == "" {
		return
	}
	out, code := s.runToCompletion(s.dir, "-C", s.dir, "teardown")
	s.Equal(0, code, "kevin teardown output:\n%s", out)
}

// TestKubectlKeepLeavesTheManifestOnTeardown proves keep: true on a
// kubectl step leaves what Up applied in place once "kevin run" tears
// its own (env) scope down - Down still runs, it just skips the delete.
func (s *KindKeepSuite) TestKubectlKeepLeavesTheManifestOnTeardown() {
	out, code := s.runUntil(s.dir, stepLine("keeper", "ready"), "-C", s.dir, "run")
	s.Require().Equal(0, code, "kevin run output:\n%s", out)
	s.Contains(out, stepLine("keeper", "removed"), "Down must still run for a keep:true step")

	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
	got, err := exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", kubeconfig, "get", "configmap", "keepme").CombinedOutput()
	s.Require().NoError(err, "keep: true must leave the manifest in place, kubectl output:\n%s", got)
	s.Contains(string(got), "keepme")
}

// fetchThroughProxyOnce is one probe attempt through the proxy, returning
// whatever body it got (even an error page) for the caller to retry on.
func (s *KindSuite) fetchThroughProxyOnce(proxyAddr, target string) string {
	pem, err := os.ReadFile(filepath.Join(s.dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	client := proxyHTTPClient(proxyAddr, newCertPool(pem))

	resp := httpGet(s.T(), client, target)
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	return readAll(s.T(), resp.Body)
}

// S3AppSuite covers a user iterating on examples/s3-app: its
// persistent cluster, intercepted S3 and cross-scope route. It runs a copy
// of the example with its fixed ports and names replaced, since the chart
// hardcodes the proxy port.
//
// Tier: e2e.
type S3AppSuite struct {
	e2eSuite

	dir  string
	name string
}

func TestS3AppSuite(t *testing.T) {
	suite.Run(t, new(S3AppSuite))
}

// copyTree copies the regular files under src into dst, keeping their
// relative paths, skipping any .kevin state.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".kevin" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (s *S3AppSuite) SetupSuite() {
	s.requireDocker()
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}

	s.name = "e2es3app"
	s.dir = s.T().TempDir()
	copyTree(s.T(), filepath.Join(repoRoot(), "examples", "s3-app"), s.dir)

	port := func() string {
		_, p, err := net.SplitHostPort(freeAddr(s.T()))
		s.Require().NoError(err)
		return p
	}
	proxyPort := port()
	rewrite := func(path string, repl ...string) {
		data, err := os.ReadFile(path)
		s.Require().NoError(err)
		out := strings.NewReplacer(repl...).Replace(string(data))
		s.Require().NoError(os.WriteFile(path, []byte(out), 0o600))
	}
	rewrite(filepath.Join(s.dir, "kevin.cue"),
		"18090", proxyPort, "18091", port(), "18092", port(),
		`"s3-app-example"`, `"kevin-e2e-s3app"`, `"s3app"`, strconv.Quote(s.name))
	rewrite(filepath.Join(s.dir, "charts", "app", "templates", "deployment.yaml"), "18090", proxyPort)
	s.cleanupProject("kevin-e2e-s3app")
}

func (s *S3AppSuite) kubectl(args ...string) (string, error) {
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", s.name)
	out, err := exec.CommandContext(s.T().Context(), "kubectl",
		append([]string{"--kubeconfig", kubeconfig}, args...)...).CombinedOutput()
	return string(out), err
}

// TestPersistentClusterSurvivesRuns proves setup's cluster and seeded
// bucket outlive a run, the app reaches the real S3 hostname through the
// interception, and a second run redeploys against the same cluster.
func (s *S3AppSuite) TestPersistentClusterSurvivesRuns() {
	require := s.Require()
	defer func() {
		p := s.startKevin(s.dir, "-C", s.dir, "teardown")
		s.Equal(0, s.waitExit(p, kindTimeout), "teardown output:\n%s", p.buf.String())
		s.Empty(s.containerIDsForProject("kevin-e2e-s3app"), "teardown must remove the cluster and MiniStack containers")
		s.requireNoKindCluster(s.name)
	}()

	setup := s.startKevin(s.dir, "-C", s.dir, "setup")
	require.Equal(0, s.waitExit(setup, kindTimeout), "setup output:\n%s", setup.buf.String())
	require.Contains(setup.buf.String(), stepLine("seed_ready", "ready"))

	for run := 1; run <= 2; run++ {
		p := s.startKevin(s.dir, "-C", s.dir, "run")
		s.waitFor(p, stepLine("app_ready", "ready"), kindTimeout)

		s.Eventually(func() bool {
			logs, _ := s.kubectl("logs", "deployment/app")
			return strings.Contains(logs, "seeded at kevin setup") && strings.Contains(logs, "heartbeat:")
		}, 2*time.Minute, 3*time.Second, "run %d: the app must read the seeded and heartbeat objects through the interception", run)

		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		require.Equal(0, s.waitExit(p, kindTimeout), "run %d output:\n%s", run, p.buf.String())

		got, err := s.kubectl("get", "deployment", "ministack")
		require.NoError(err, "the persistent scope must survive run %d:\n%s", run, got)
		got, err = s.kubectl("get", "deployment", "app")
		require.Error(err, "the env-scope app must be removed by run %d, got:\n%s", run, got)
		s.Contains(got, "NotFound")
	}
}
