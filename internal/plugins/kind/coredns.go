package kind

import (
	"context"
	"fmt"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

// adminKubeconfig is the kubeconfig that a node carries for its own cluster.
const adminKubeconfig = "/etc/kubernetes/admin.conf"

// patchCoreDNS adds a forward zone for domain to the CoreDNS Corefile of the
// cluster, and points every node's own /etc/resolv.conf at relay too.
// patchCoreDNS is idempotent: it replaces an existing zone for domain
// instead of adding one, and a resolv.conf rewrite is naturally idempotent
// too.
func patchCoreDNS(ctx context.Context, allNodes []string, domain, relay string, out plugin.Emitter) error {
	container, err := bootstrapControlPlaneNode(allNodes)
	if err != nil {
		return fmt.Errorf("kind: find the control plane node: %w", err)
	}

	out.Log("stdout", "patching coredns for "+domain)
	out.Progress("patching coredns", 0, 0)

	current, err := kubectl(ctx, container, "-n", "kube-system", "get", "configmap", "coredns",
		"-o", "jsonpath={.data.Corefile}")
	if err != nil {
		return fmt.Errorf("kind: read the coredns Corefile: %w", err)
	}

	next := corefileWithZone(current, domain, relay)

	manifest, err := kubectl(ctx, container, "create", "configmap", "coredns",
		"-n", "kube-system", "--from-literal=Corefile="+next, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return fmt.Errorf("kind: render the coredns configmap: %w", err)
	}

	if _, err = kubectlInput(ctx, container, strings.NewReader(manifest),
		"-n", "kube-system", "replace", "-f", "-"); err != nil {
		return fmt.Errorf("kind: replace the coredns configmap: %w", err)
	}

	// CoreDNS's own default "." zone forwards anything outside cluster.local
	// to the node's own resolv.conf (kubelet wires it in directly under
	// dnsPolicy: Default) - rewriting that file reaches the relay for
	// anything else without an edit to that zone, which also runs the
	// kubernetes plugin cluster.local depends on.
	if err = pointNodeDNSAtRelay(ctx, allNodes, relay); err != nil {
		return err
	}

	out.Log("stdout", "restarting coredns")
	if _, err = kubectl(ctx, container, "-n", "kube-system", "rollout", "restart", "deployment/coredns"); err != nil {
		return fmt.Errorf("kind: restart coredns: %w", err)
	}
	if _, err = kubectl(ctx, container, "-n", "kube-system", "rollout", "status",
		"deployment/coredns", "--timeout=60s"); err != nil {
		return fmt.Errorf("kind: wait for coredns: %w", err)
	}

	out.Log("stdout", "coredns resolves *."+domain)
	return nil
}

// pointNodeDNSAtRelay rewrites every node's own /etc/resolv.conf to name
// relay as its only nameserver.
func pointNodeDNSAtRelay(ctx context.Context, allNodes []string, relay string) error {
	for _, node := range allNodes {
		if _, err := dockerClient.Exec(ctx, node, "sh", "-c", "echo nameserver "+relay+" > /etc/resolv.conf"); err != nil {
			return fmt.Errorf("kind: point %s's dns at the relay: %w", node, err)
		}
	}
	return nil
}

// kubectl runs kubectl inside a node, against the cluster of that node.
func kubectl(ctx context.Context, container string, args ...string) (string, error) {
	return dockerClient.Exec(ctx, container, kubectlArgs(args)...)
}

// kubectlInput runs kubectl inside a node, with stdin feeding the command.
func kubectlInput(ctx context.Context, container string, stdin *strings.Reader, args ...string) (string, error) {
	return dockerClient.ExecInput(ctx, container, stdin, kubectlArgs(args)...)
}

// kubectlArgs prepends the kubectl command and the admin kubeconfig flag to
// args.
func kubectlArgs(args []string) []string {
	full := make([]string, 0, len(args)+2)
	full = append(full, "kubectl", "--kubeconfig", adminKubeconfig)
	return append(full, args...)
}
