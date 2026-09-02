//go:build e2e

package e2e

import (
	"fmt"
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
// nodes join both the kind network and kevin's shared network, a
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
			expose: [{port: 5000}]
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
		uses:  "builtin:kind"
		label: "Kind Cluster"
		needs: ["registry"]
		with: {
			workers: 1
			wait:    "5m"
			egress:  ["docker.io", "*.docker.io", "*.docker.com"]
			expose:  [{name: "apiserver", address: "kubernetes.default.svc:443"}]
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
}
`

// KindSuite covers docs/MANUAL_TESTING.md sections 7 (builtin:kind,
// builtin:kubectl, builtin:helm, relay routing) and 9 (kevin connect).
// SetupSuite brings up one cluster - by far the most expensive part of the
// whole e2e run - and TearDownSuite tears it down once; merging the two
// sections onto it avoids paying kind's slow bring-up twice.
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
	s.writeCUE(s.dir, src)
	s.cleanupProject(project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("app_route", "ready"), kindTimeout)
	s.waitFor(s.p, stepLine("chart_ready", "ready"), kindTimeout)
	s.out = s.p.buf.String()
}

func (s *KindSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	require := s.Require()
	require.NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.waitExit(s.p, kindTimeout)
}

// TestClusterAndDeploymentsReady confirms every step in the cluster's own
// DAG reached ready: the registry, the cluster, both readiness gates, the
// kubectl Deployment, and the Helm chart.
func (s *KindSuite) TestClusterAndDeploymentsReady() {
	for _, step := range []string{
		"registry", "registry_ready", "cluster", "apiserver_ready",
		"app", "app_ready", "chart", "chart_ready", "app_route",
	} {
		s.Contains(s.out, stepLine(step, "ready"), "%s must reach ready", step)
	}
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

// TestConnectExecsCommandWithExportedEnv covers "kevin connect cluster --
// <cmd>": it runs the given command with KUBECONFIG (and any other
// exported vars) set.
func (s *KindSuite) TestConnectExecsCommandWithExportedEnv() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	out, code := s.runToCompletion(s.dir, "-C", s.dir, "connect", "cluster", "--", "kubectl", "get", "pods", "-A")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "kube-system")
}

// TestConnectWithNoNameDefaultsToTheOnlyExportableStep covers "kevin
// connect" with no step named: since only cluster supports connect in this
// environment, it behaves the same as naming it explicitly.
func (s *KindSuite) TestConnectWithNoNameDefaultsToTheOnlyExportableStep() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	out, code := s.runToCompletion(s.dir, "-C", s.dir, "connect", "--", "kubectl", "get", "nodes")
	s.Equal(0, code, "output:\n%s", out)
	s.Contains(out, "Ready")
}

// noExportStepCUE is a minimal single-step DAG using "echo:probe" - unlike
// "echo:echo", the probe step type implements no Export, so it never shows
// up as a connect candidate.
const noExportStepCUE = `project: "%s"

plugins: echo: cmd: %s

env: a: {
	uses:  "echo:probe"
	label: "A"
}
`

// TestConnectErrorsCleanlyWithNoConnectableStep covers "kevin connect"
// against an environment where no step supports connect - errors cleanly,
// not a crash. A fresh, separate plain project: echo:probe implements no
// Export.
func (s *KindSuite) TestConnectErrorsCleanlyWithNoConnectableStep() {
	dir := s.T().TempDir()
	src := fmt.Sprintf(noExportStepCUE, "kevin-e2e-kind-noconnect", strconv.Quote(s.echoPluginBin()))
	s.writeCUE(dir, src)

	out, code := s.runToCompletion(dir, "-C", dir, "connect")
	s.NotEqual(0, code, "output:\n%s", out)
	s.Contains(out, "no step")
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
	uses:  "builtin:kind"
	label: "Kind Cluster"
	with: {
		workers: 0
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

// KindKeepSuite covers docs/MANUAL_TESTING.md section 7's kubectl/helm
// keep: field - its own suite, and its own setup-scope cluster, because
// proving keep needs a real "kevin run" teardown to happen (KindSuite's
// shared cluster never runs one mid-suite).
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
	s.writeCUE(s.dir, fmt.Sprintf(keepCUE, s.project))
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
