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

// minikubeTimeout covers a cold minikube start, the slowest bring-up in this
// suite.
const minikubeTimeout = 10 * defaultTimeout

// minikubeCUE mirrors examples/minikube/kevin.cue, plus a probe Pod that
// dials a cluster Service and an unregistered external host.
const minikubeCUE = `project: "%s"

env: {
	cluster: {
		uses:  "builtin:kubernetes"
		label: "minikube Cluster"
		with: {
			driver: "minikube"
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

// MinikubeSuite covers the minikube driver the way KindSuite covers kind:
// one cluster for the whole suite, torn down by SIGINT in TearDownSuite,
// where it also checks that no node container survives.
type MinikubeSuite struct {
	e2eSuite

	dir     string
	project string
	p       *kevinProc
	out     string
}

func TestMinikubeSuite(t *testing.T) {
	suite.Run(t, new(MinikubeSuite))
}

func (s *MinikubeSuite) SetupSuite() {
	s.requireDocker()
	if _, err := exec.LookPath("minikube"); err != nil {
		s.T().Skip("minikube not found on PATH")
	}

	s.project = "kevin-e2e-minikube"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(minikubeCUE, s.project))
	s.cleanupProject(s.project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("app_route", "ready"), minikubeTimeout)
	s.waitFor(s.p, stepLine("capture_probe_done", "ready"), minikubeTimeout)
	s.out = s.p.buf.String()
}

func (s *MinikubeSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	s.Require().NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.waitExit(s.p, minikubeTimeout)
	s.Empty(s.containerIDsForProject(s.project), "teardown must remove the control plane and every worker")
}

// TestClusterAndDeploymentsReady confirms every step reached ready.
func (s *MinikubeSuite) TestClusterAndDeploymentsReady() {
	for _, step := range []string{
		"cluster", "apiserver_ready", "app", "app_ready", "app_route",
		"capture_probe", "capture_probe_done",
	} {
		s.Contains(s.out, stepLine(step, "ready"), "%s must reach ready", step)
	}
}

// TestKubectlGetNodesThroughWrittenKubeconfig proves the control plane and
// the worker both joined and report Ready through the published kubeconfig.
func (s *MinikubeSuite) TestKubectlGetNodesThroughWrittenKubeconfig() {
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

// TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic is the
// minikube counterpart of the kind capture test: capture_probe dials a
// cluster Service and an unregistered external host.
func (s *MinikubeSuite) TestNodeLevelCaptureRedirectsPodEgressButSparesClusterTraffic() {
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
func (s *MinikubeSuite) TestAppRouteReachesTheServiceThroughTheRelay() {
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

func (s *MinikubeSuite) kubeconfig() string {
	return filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
}
