//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// k3dTimeout covers a cold k3d start, the slowest bring-up in this
// suite.
const k3dTimeout = 10 * defaultTimeout

// k3dCUE mirrors examples/k3d/kevin.cue.
const k3dCUE = `project: "%s"

proxy: egress: allow: ["docker.io", "*.docker.io", "*.docker.com"]

env: {
	cluster: {
		uses:  "builtin:kubernetes"
		label: "k3d Cluster"
		with: {
			driver: "k3d"
			workers: worker_a: {}
			wait:   "5m"
			egress: ["docker.io", "*.docker.io", "*.docker.com"]
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
`

// K3dSuite covers the k3d driver:
// one cluster for the whole suite, torn down by SIGINT in TearDownSuite,
// where it also checks that no node container survives.
type K3dSuite struct {
	e2eSuite

	dir     string
	project string
	p       *kevinProc
	out     string
}

func TestK3dSuite(t *testing.T) {
	suite.Run(t, new(K3dSuite))
}

func (s *K3dSuite) SetupSuite() {
	s.requireDocker()
	if _, err := exec.LookPath("k3d"); err != nil {
		s.T().Skip("k3d not found on PATH")
	}

	s.project = "kevin-e2e-k3d"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(k3dCUE, s.project))
	s.cleanupProject(s.project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("app_route", "ready"), k3dTimeout)
	s.waitFor(s.p, stepLine("capture_probe_done", "ready"), k3dTimeout)
	s.out = s.p.buf.String()
}

func (s *K3dSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	s.Require().NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.waitExit(s.p, k3dTimeout)
	s.Empty(s.containerIDsForProject(s.project), "teardown must remove the control plane and every worker")
}

// TestClusterAndDeploymentsReady confirms every step reached ready.
func (s *K3dSuite) TestClusterAndDeploymentsReady() {
	for _, step := range []string{
		"cluster", "apiserver_ready", "app", "app_ready", "app_route",
		"capture_probe", "capture_probe_done",
	} {
		s.Contains(s.out, stepLine(step, "ready"), "%s must reach ready", step)
	}
}

// TestKubectlGetNodesThroughWrittenKubeconfig proves the control plane and
// the worker both joined and report Ready through the published kubeconfig.
func (s *K3dSuite) TestKubectlGetNodesThroughWrittenKubeconfig() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	out, err := exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", s.kubeconfig(), "get", "nodes", "--no-headers").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	s.Len(lines, 2, "a control plane and one worker, got:\n%s", out)
	for _, line := range lines {
		s.Contains(line, " Ready", "node must be Ready: %s", line)
	}
}

// TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic checks that
// capture_probe dials a cluster Service and an unregistered external host, and
// only the external request is redirected to the proxy.
func (s *K3dSuite) TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		s.T().Skip("kubectl not found on PATH")
	}
	out, err := exec.CommandContext(s.T().Context(), "kubectl",
		"--kubeconfig", s.kubeconfig(), "logs", "pod/capture-probe").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)

	inCluster, external, ok := strings.Cut(string(out), "external:\n")
	s.Require().True(ok, "capture-probe log must carry both probes, got:\n%s", out)

	const denyMarker = "kevin blocked a request to"
	s.NotContains(inCluster, denyMarker, "a pod's request to a cluster Service must reach it directly")
	s.Contains(external, denyMarker, "a pod's request to an unregistered external host must land on the proxy")
}

// TestAppRouteReachesTheServiceThroughTheRelay proves app.kevin.home reaches
// the nginx Service through the cluster's SOCKS5 relay.
func (s *K3dSuite) TestAppRouteReachesTheServiceThroughTheRelay() {
	var proxyAddr string
	for _, row := range addrRE.FindAllStringSubmatch(s.out, -1) {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr)

	pem, err := os.ReadFile(filepath.Join(s.dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	client := proxyHTTPClient(proxyAddr, newCertPool(pem))

	// kube-proxy can lag the Deployment's rollout status, so retry a 502
	// briefly before failing.
	var body string
	s.Require().Eventually(func() bool {
		resp := httpGet(s.T(), client, "https://app.kevin.home/")
		defer resp.Body.Close() //nolint:errcheck // read-only response body
		body = readAll(s.T(), resp.Body)
		return strings.Contains(body, "Welcome to nginx")
	}, 30*time.Second, time.Second, "must reach the nginx pod through the relay-routed Service, last body:\n%s", body)
}

func (s *K3dSuite) kubeconfig() string {
	return filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
}

// k3dKeepCUE puts the cluster in the setup scope and a kubectl step
// with keep: true in the env scope.
const k3dKeepCUE = `project: "%s"

proxy: egress: allow: ["docker.io", "*.docker.io", "*.docker.com"]

setup: cluster: {
	uses:  "builtin:kubernetes"
	label: "k3d Cluster"
	with: {
		driver: "k3d"
		workers: {}
		wait: "5m"
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

// K3dKeepSuite covers a k3d cluster in the setup scope: keep: true
// surviving a run, reuse by a second setup, and recreation when the proxy
// address changes.
type K3dKeepSuite struct {
	e2eSuite

	dir     string
	project string
}

func TestK3dKeepSuite(t *testing.T) {
	suite.Run(t, new(K3dKeepSuite))
}

func (s *K3dKeepSuite) SetupSuite() {
	s.requireDocker()
	for _, bin := range []string{"k3d", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			s.T().Skip(bin + " not found on PATH")
		}
	}

	s.project = "kevin-e2e-k3d-keep"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(k3dKeepCUE, s.project))
	s.cleanupProject(s.project)

	out, code := s.setup()
	s.Require().Equal(0, code, "kevin setup output:\n%s", out)
	s.Require().Contains(out, stepLine("cluster", "ready"))
}

func (s *K3dKeepSuite) TearDownSuite() {
	if s.dir == "" {
		return
	}
	out, code := s.runToCompletionWithTimeout(k3dTimeout, "teardown")
	s.Equal(0, code, "kevin teardown output:\n%s", out)
	s.Empty(s.containerIDsForProject(s.project), "teardown must remove the control plane")
}

// TestSetupScopeLifecycle runs in order on one cluster: keep: true leaves
// the manifest after run, an unchanged setup reuses the cluster and the
// manifest, and a changed proxy address recreates it without the manifest.
func (s *K3dKeepSuite) TestSetupScopeLifecycle() {
	s.Run("keep leaves the manifest on teardown", func() {
		out, code := s.runUntil(s.dir, stepLine("keeper", "ready"), "-C", s.dir, "run")
		s.Require().Equal(0, code, "kevin run output:\n%s", out)
		s.Contains(out, stepLine("keeper", "removed"), "Down must still run for a keep:true step")
		s.Require().True(s.hasKeepme(), "keep: true must leave the manifest in place")
	})

	s.Run("setup again reuses the cluster", func() {
		out, code := s.setup()
		s.Require().Equal(0, code, "kevin setup output:\n%s", out)
		s.NotContains(out, s.creating(), "an unchanged setup must not create a cluster")
		s.True(s.hasKeepme(), "reuse must not destroy the cluster")
	})

	s.Run("a new proxy address recreates the cluster", func() {
		s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(k3dKeepCUE, s.project))
		out, code := s.setup()
		s.Require().Equal(0, code, "kevin setup output:\n%s", out)
		s.Contains(out, s.creating(), "a changed proxy address must create a new cluster")
		s.False(s.hasKeepme(), "a recreated cluster must not carry the old manifest")
	})
}

func (s *K3dKeepSuite) setup() (string, int) {
	return s.runToCompletionWithTimeout(k3dTimeout, "setup")
}

func (s *K3dKeepSuite) runToCompletionWithTimeout(timeout time.Duration, args ...string) (string, int) {
	p := s.startKevin(s.dir, append([]string{"-C", s.dir}, args...)...)
	code := s.waitExit(p, timeout)
	return p.buf.String(), code
}

func (s *K3dKeepSuite) hasKeepme() bool {
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
	return exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", kubeconfig, "get", "configmap", "keepme").Run() == nil
}

// creating is the progress line the k3d driver prints while it creates
// the cluster, and only then.
func (s *K3dKeepSuite) creating() string {
	return "creating " + s.project + "-cluster"
}
