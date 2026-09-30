package kubernetes

import (
	"context"
	"fmt"
	"strings"

	"github.com/justenwalker/kevin/internal/corefile"
	"github.com/justenwalker/kevin/plugin"
)

// wantsCoreDNSPatch reports whether Up must patch the cluster DNS. A relay
// that is off, or an environment with no domain, needs no patch.
func wantsCoreDNSPatch(cfg config, env plugin.Env) bool {
	return cfg.CoreDNS && env.Relay != "" && env.Domain != ""
}

// patchCoreDNS adds a forward zone for domain to the CoreDNS Corefile of the
// cluster, and points every node's own /etc/resolv.conf at relay too.
// patchCoreDNS is idempotent: it replaces an existing zone for domain
// instead of adding one, and a resolv.conf rewrite is naturally idempotent
// too.
func patchCoreDNS(ctx context.Context, drv driver, allNodes []string, domain, relay string, out plugin.Emitter) error {
	out.Log("stdout", "patching coredns for "+domain)
	out.Progress("patching coredns", 0, 0)

	current, err := drv.Kubectl(ctx, "-n", "kube-system", "get", "configmap", "coredns",
		"-o", "jsonpath={.data.Corefile}")
	if err != nil {
		return fmt.Errorf("kubernetes: read the coredns Corefile: %w", err)
	}

	next := corefile.WithZone(current, domain, relay)

	manifest, err := drv.Kubectl(ctx, "create", "configmap", "coredns",
		"-n", "kube-system", "--from-literal=Corefile="+next, "--dry-run=client", "-o", "yaml")
	if err != nil {
		return fmt.Errorf("kubernetes: render the coredns configmap: %w", err)
	}

	if _, err = drv.KubectlInput(ctx, strings.NewReader(manifest),
		"-n", "kube-system", "replace", "-f", "-"); err != nil {
		return fmt.Errorf("kubernetes: replace the coredns configmap: %w", err)
	}

	// CoreDNS's own default "." zone forwards anything outside cluster.local
	// to the node's own resolv.conf (kubelet wires it in directly under
	// dnsPolicy: Default) - rewriting that file reaches the relay for
	// anything else without an edit to that zone, which also runs the
	// kubernetes plugin cluster.local depends on.
	if err = drv.PointDNSAtRelay(ctx, allNodes, relay); err != nil {
		return err
	}

	out.Log("stdout", "restarting coredns")
	if _, err = drv.Kubectl(ctx, "-n", "kube-system", "rollout", "restart", "deployment/coredns"); err != nil {
		return fmt.Errorf("kubernetes: restart coredns: %w", err)
	}
	if _, err = drv.Kubectl(ctx, "-n", "kube-system", "rollout", "status",
		"deployment/coredns", "--timeout=60s"); err != nil {
		return fmt.Errorf("kubernetes: wait for coredns: %w", err)
	}

	out.Log("stdout", "coredns resolves *."+domain)
	return nil
}
