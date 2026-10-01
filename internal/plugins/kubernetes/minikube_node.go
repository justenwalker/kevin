package kubernetes

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

// minikubeCAPath is where minikube installs a certificate from the certs
// directory of MINIKUBE_HOME in every node, before containerd starts.
const minikubeCAPath = "/etc/ssl/certs/kevin-root.pem"

// containerdProxyDropIn is the systemd drop-in that gives containerd the
// proxy variables. minikube drops its --docker-env flag under containerd.
const containerdProxyDropIn = "/etc/systemd/system/containerd.service.d/http-proxy.conf"

// setContainerdProxy gives containerd on one node the proxy variables, and
// restarts it.
func (d *minikubeDriver) setContainerdProxy(ctx context.Context, container string, proxy map[string]string) error {
	var unit strings.Builder
	unit.WriteString("[Service]\n")
	for _, key := range slices.Sorted(maps.Keys(proxy)) {
		fmt.Fprintf(&unit, "Environment=%q\n", key+"="+proxy[key])
	}

	if _, err := d.rt.Exec(ctx, container, "mkdir", "-p", path.Dir(containerdProxyDropIn)); err != nil {
		return fmt.Errorf("kubernetes: minikube: create the containerd drop-in directory on %s: %w", container, err)
	}
	if _, err := d.rt.ExecInput(ctx, container, strings.NewReader(unit.String()), "tee", containerdProxyDropIn); err != nil {
		return fmt.Errorf("kubernetes: minikube: write the containerd proxy on %s: %w", container, err)
	}
	if _, err := d.rt.Exec(ctx, container, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("kubernetes: minikube: reload systemd on %s: %w", container, err)
	}
	if _, err := d.rt.Exec(ctx, container, "systemctl", "restart", "containerd"); err != nil {
		return fmt.Errorf("kubernetes: minikube: restart containerd on %s: %w", container, err)
	}
	return waitContainerdReady(ctx, d.rt, container)
}

// TrustCA checks that every node holds the kevin root certificate, which
// minikube installed from the certs directory of MINIKUBE_HOME.
func (d *minikubeDriver) TrustCA(ctx context.Context, allNodes []string, caPEM string, out plugin.Emitter) error {
	out.Log("stdout", "checking that the nodes hold the kevin root certificate")
	out.Progress("trusting the kevin ca", 0, 0)

	for _, node := range allNodes {
		installed, err := d.rt.Exec(ctx, node, "cat", minikubeCAPath)
		if err != nil {
			return fmt.Errorf("kubernetes: minikube: read the kevin root certificate on %s: %w", node, err)
		}
		if !strings.Contains(normalizePEM(installed), normalizePEM(caPEM)) {
			return fmt.Errorf("kubernetes: minikube: %s: %w", node, ErrNotTrusted)
		}
	}

	out.Log("stdout", "the nodes trust the kevin root certificate")
	return nil
}

// PointDNSAtRelay rewrites every node's own /etc/resolv.conf to name relay
// as its only nameserver.
func (d *minikubeDriver) PointDNSAtRelay(ctx context.Context, allNodes []string, relay string) error {
	for _, node := range allNodes {
		if _, err := d.rt.Exec(ctx, node, "sh", "-c", "echo nameserver "+relay+" > /etc/resolv.conf"); err != nil {
			return fmt.Errorf("kubernetes: minikube: point %s's dns at the relay: %w", node, err)
		}
	}
	return nil
}

// minikubeAPIPort is the container port that the API server listens on.
const minikubeAPIPort = "8443/tcp"

// kubeconfigServer matches the server address of the cluster in a kubeconfig.
var kubeconfigServer = regexp.MustCompile(`(?m)^(\s*server: )https://\S+$`)

// RefreshAccess points the kubeconfig at the host port that the control plane
// publishes the API server on.
func (d *minikubeDriver) RefreshAccess(ctx context.Context) error {
	if d.rt == nil {
		return fmt.Errorf("kubernetes: minikube: refresh access: %w", ErrNoRuntime)
	}
	info, err := d.rt.Inspect(ctx, d.ControlPlane())
	if err != nil {
		return fmt.Errorf("kubernetes: minikube: inspect %s: %w", d.ControlPlane(), err)
	}
	addr, ok := info.Ports[minikubeAPIPort]
	if !ok {
		return fmt.Errorf("kubernetes: minikube: %s publishes no %s: %w", d.ControlPlane(), minikubeAPIPort, ErrNoAPIPort)
	}

	raw, err := os.ReadFile(d.kubeconfig)
	if err != nil {
		return fmt.Errorf("kubernetes: minikube: read the kubeconfig: %w", err)
	}
	updated := kubeconfigServer.ReplaceAll(raw, []byte("${1}https://"+addr))
	//nolint:gosec // the path is under the workspace that kevin owns
	if err = os.WriteFile(d.kubeconfig, updated, 0o600); err != nil {
		return fmt.Errorf("kubernetes: minikube: write the kubeconfig: %w", err)
	}
	return nil
}

// LabelNodes sets the kevin.node label on every node. minikube has no flag
// for a label per node. A worker's Kubernetes node is named
// "<cluster>-m02", "<cluster>-m03", and so on, in the order of its key.
func (d *minikubeDriver) LabelNodes(ctx context.Context) error {
	nodes := map[string]string{d.ControlPlane(): controlPlaneNodeName}
	for i, worker := range d.workers() {
		nodes[fmt.Sprintf("%s-m%02d", d.name, i+2)] = worker //nolint:mnd // the first worker is the second node
	}
	for _, node := range slices.Sorted(maps.Keys(nodes)) {
		if _, err := d.Kubectl(ctx, "label", "node", node, nodeLabelKey+"="+nodes[node], "--overwrite"); err != nil {
			return fmt.Errorf("kubernetes: minikube: label node %s: %w", node, err)
		}
	}
	return nil
}
