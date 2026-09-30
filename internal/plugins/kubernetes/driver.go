package kubernetes

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

// createSpec is what a driver needs at cluster creation beyond its own
// config: the relay ports to publish on the control-plane node, which must
// be baked in before creation, and how long to wait for the cluster.
type createSpec struct {
	Ports clusterrelay.Ports
	Wait  time.Duration
}

// driver creates and inspects one cluster with a cluster tool. The rest of
// the step (relay, CoreDNS, capture, outputs, reuse) is the same for every
// driver and lives in this package's core files.
type driver interface {
	clusterLifecycle
	kubectlRunner
	nodeSetup
}

// clusterLifecycle creates, finds, and removes the cluster.
type clusterLifecycle interface {
	// Nodes lists the container names of the cluster's nodes, or none when
	// the cluster does not exist.
	Nodes(ctx context.Context) ([]string, error)

	// Fingerprint reports everything that Create would build the cluster
	// from, as one comparable string. A cluster is reused when the
	// fingerprint of its last Create equals this one.
	Fingerprint(spec createSpec) (string, error)

	// Create removes any previous cluster of the same name, then creates a
	// new one on the project network, and returns its nodes.
	Create(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error)

	// Delete removes the cluster. It succeeds when there is none.
	Delete(ctx context.Context, out plugin.Emitter) error

	// Context is the kubeconfig context name of the cluster.
	Context() string

	// ControlPlane is the Kubernetes node name of the control-plane node,
	// which the relay pod runs on.
	ControlPlane() string
}

// kubectlRunner runs kubectl against the cluster.
type kubectlRunner interface {
	// Kubectl runs kubectl against the cluster.
	Kubectl(ctx context.Context, args ...string) (string, error)

	// KubectlInput runs kubectl against the cluster, with stdin feeding it.
	KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error)
}

// nodeSetup changes the nodes of a running cluster.
type nodeSetup interface {
	// RefreshAccess updates the kubeconfig for the address that the cluster
	// answers on after its nodes joined the project network, which can
	// change the host port that the API server is published on.
	RefreshAccess(ctx context.Context) error

	// LabelNodes sets the kevin.node label on nodes that the driver could
	// not label at creation.
	LabelNodes(ctx context.Context) error

	// TrustCA installs the kevin root certificate into the nodes, and
	// checks that they hold it.
	TrustCA(ctx context.Context, nodes []string, caPEM string, out plugin.Emitter) error

	// PointDNSAtRelay makes relay the only nameserver of the nodes.
	PointDNSAtRelay(ctx context.Context, nodes []string, relay string) error

	// LoadImage loads the image archive at path into the nodes.
	LoadImage(ctx context.Context, path string, out plugin.Emitter) error
}

// newDriver builds the driver that cfg names for one cluster. rt may be nil
// for a caller that only removes or inspects the cluster.
func newDriver(cfg config, env plugin.Env, name, kubeconfig string, rt cri.Runtime) (driver, error) { //nolint:ireturn // the driver is the seam: which one is only known from the with block
	switch cfg.Driver {
	case "kind":
		return newKindDriver(cfg, env, name, kubeconfig, rt), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, cfg.Driver)
	}
}

// enginePodman is the plugin.Env.Engine value for Podman.
const enginePodman = "podman"
