//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// minikubeTimeout covers a cold minikube start, the slowest bring-up in this
// suite.
const minikubeTimeout = 10 * defaultTimeout

// minikubeCUE is a bare minikube cluster: enough to prove the driver is selectable
// through the binary.
const minikubeCUE = `project: "%s"

env: cluster: {
	uses:  "builtin:kubernetes"
	label: "minikube Cluster"
	with: {
		driver: "minikube"
		wait:   "5m"
		egress: ["docker.io", "*.docker.io", "*.docker.com"]
	}
}
`

// MinikubeSuite covers a user selecting the minikube driver:
// the cluster comes up and kubectl reaches it through the kubeconfig kevin
// writes. What the driver builds is checked by the integration suite in
// internal/plugins/kubernetes.
//
// Tier: e2e.
type MinikubeSuite struct {
	e2eSuite

	dir     string
	project string
	p       *kevinProc
}

func TestMinikubeSuite(t *testing.T) {
	suite.Run(t, new(MinikubeSuite))
}

func (s *MinikubeSuite) SetupSuite() {
	s.requireDocker()
	for _, bin := range []string{"minikube", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			s.T().Skip(bin + " not found on PATH")
		}
	}

	s.project = "kevin-e2e-minikube"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(minikubeCUE, s.project))
	s.cleanupProject(s.project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("cluster", "ready"), minikubeTimeout)
}

func (s *MinikubeSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	s.Require().NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(s.p, minikubeTimeout), "output:\n%s", s.p.buf.String())
}

// TestKubectlGetNodesThroughWrittenKubeconfig proves the published
// kubeconfig reaches a Ready node.
func (s *MinikubeSuite) TestKubectlGetNodesThroughWrittenKubeconfig() {
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
	out, err := exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		s.Contains(line, " Ready", "node must be Ready: %s", line)
	}
}
