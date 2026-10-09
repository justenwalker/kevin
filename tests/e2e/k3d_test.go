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

// k3dTimeout covers a cold k3d start, the slowest bring-up in this
// suite.
const k3dTimeout = 10 * defaultTimeout

// k3dCUE is a bare k3d cluster: enough to prove the driver is selectable
// through the binary.
const k3dCUE = `project: "%s"

proxy: egress: allow: ["docker.io", "*.docker.io", "*.docker.com"]

env: cluster: {
	uses:  "builtin:kubernetes"
	label: "k3d Cluster"
	with: {
		driver: "k3d"
		wait:   "5m"
		egress: ["docker.io", "*.docker.io", "*.docker.com"]
	}
}
`

// K3dSuite covers a user selecting the k3d driver:
// the cluster comes up and kubectl reaches it through the kubeconfig kevin
// writes. What the driver builds is checked by the integration suite in
// internal/plugins/kubernetes.
//
// Tier: e2e.
type K3dSuite struct {
	e2eSuite

	dir     string
	project string
	p       *kevinProc
}

func TestK3dSuite(t *testing.T) {
	suite.Run(t, new(K3dSuite))
}

func (s *K3dSuite) SetupSuite() {
	s.requireDocker()
	for _, bin := range []string{"k3d", "kubectl"} {
		if _, err := exec.LookPath(bin); err != nil {
			s.T().Skip(bin + " not found on PATH")
		}
	}

	s.project = "kevin-e2e-k3d"
	s.dir = s.T().TempDir()
	s.writeCUE(s.dir, proxyBlock(s.T())+fmt.Sprintf(k3dCUE, s.project))
	s.cleanupProject(s.project)

	s.p = s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(s.p, stepLine("cluster", "ready"), k3dTimeout)
}

func (s *K3dSuite) TearDownSuite() {
	if s.p == nil {
		return
	}
	s.Require().NoError(s.p.cmd.Process.Signal(syscall.SIGINT))
	s.Equal(0, s.waitExit(s.p, k3dTimeout), "output:\n%s", s.p.buf.String())
}

// TestKubectlGetNodesThroughWrittenKubeconfig proves the published
// kubeconfig reaches a Ready node.
func (s *K3dSuite) TestKubectlGetNodesThroughWrittenKubeconfig() {
	kubeconfig := filepath.Join(s.dir, ".kevin", "kubeconfig", s.project+"-cluster")
	out, err := exec.CommandContext(s.T().Context(), "kubectl", "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers").CombinedOutput()
	s.Require().NoError(err, "output:\n%s", out)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		s.Contains(line, " Ready", "node must be Ready: %s", line)
	}
}
