package kind

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

// wantsCapture reports whether the cluster's nodes should register for
// transparent egress capture - whenever a relay exists for the
// environment, unconditionally, matching builtin:container's own
// mandatory, no-opt-out capture.
func wantsCapture(env plugin.Env) bool {
	return env.Relay != ""
}

// netnsTargets builds one capture registration per node of the cluster -
// the node's own network namespace, and the CIDRs it must never redirect.
// Returns (nil, nil) when CIDR discovery fails - see podAndServiceCIDRs.
func netnsTargets(ctx context.Context, stepName string, allNodes []string, out plugin.Emitter) ([]plugin.NetnsTarget, error) {
	controlPlane, err := bootstrapControlPlaneNode(allNodes)
	if err != nil {
		return nil, fmt.Errorf("kind: capture: %w", err)
	}

	exclude, err := podAndServiceCIDRs(ctx, controlPlane)
	if err != nil {
		// A verified exclusion list is what makes capture safe at all - not
		// finding one is a reason to skip capture for this cluster, not to
		// fail Up over it.
		out.Log("stdout", "skipping egress capture: "+err.Error())
		return nil, nil //nolint:nilerr // deliberate fail-open, see the comment above
	}

	targets := make([]plugin.NetnsTarget, 0, len(allNodes))
	for _, node := range allNodes {
		info, err := dockerClient.Inspect(ctx, node)
		if err != nil {
			return nil, fmt.Errorf("kind: capture: inspect %s: %w", node, err)
		}
		if info.NetnsPath == "" {
			continue
		}
		targets = append(targets, plugin.NetnsTarget{
			ID:           stepName + "/" + node,
			NetnsPath:    info.NetnsPath,
			ExcludeCIDRs: exclude,
		})
	}
	return targets, nil
}

// podAndServiceCIDRs reads the cluster's pod and service subnets from
// kubeadm's own ClusterConfiguration configmap - the authoritative record
// of the effective values regardless of whether kevin generated the
// cluster config or a step's own with.config override set them, and
// available from very early in cluster bring-up (kubeadm writes it during
// "kubeadm init", well before CoreDNS or the CNI itself finish
// initializing).
//
// An error here - kubectl failing, neither field present, or an entry that
// doesn't parse as a CIDR - is always a reason for the caller to skip
// capture for this cluster, never to fail Up over it: a verified exclusion
// list is what makes capture safe at all, and a wrong or missing one would
// silently break pod-to-pod/pod-to-service traffic, which is worse than no
// capture.
func podAndServiceCIDRs(ctx context.Context, controlPlaneNode string) ([]string, error) {
	out, err := kubectl(ctx, controlPlaneNode, "-n", "kube-system", "get", "configmap", "kubeadm-config",
		"-o", "jsonpath={.data.ClusterConfiguration}")
	if err != nil {
		return nil, fmt.Errorf("kind: read kubeadm-config: %w", err)
	}

	pod := clusterConfigValue(out, "podSubnet")
	service := clusterConfigValue(out, "serviceSubnet")
	if len(pod) == 0 || len(service) == 0 {
		return nil, fmt.Errorf("kind: %w", ErrNoClusterCIDRs)
	}

	cidrs := append(append([]string{}, pod...), service...)
	for _, cidr := range cidrs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return nil, fmt.Errorf("kind: kubeadm-config reports %q as a subnet: %w", cidr, err)
		}
	}
	return cidrs, nil
}

// clusterConfigValue returns the comma-split value of a top-level
// "key: value" line in a kubeadm ClusterConfiguration's YAML text - not a
// real YAML parse, since the networking block's podSubnet/serviceSubnet
// are always this flat, trusted, single-line shape (comma-separated for a
// dual-stack cluster).
func clusterConfigValue(text, key string) []string {
	prefix := key + ":"
	for line := range strings.SplitSeq(text, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		return strings.Split(value, ",")
	}
	return nil
}
