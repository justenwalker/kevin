package kind

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

// wantsCapture reports whether the cluster's nodes should register for
// transparent egress capture - whenever a relay exists for the
// environment, unconditionally, matching builtin:container's own
// mandatory, no-opt-out capture.
func wantsCapture(env plugin.Env) bool {
	return env.Relay != ""
}

// nodeContainers reports one ContainerInfo per node of the cluster - its
// own network namespace, and the CIDRs it must never redirect when the
// relay captures it as a router. Returns (nil, nil) when CIDR discovery
// fails - see podAndServiceCIDRs.
// out may be nil - Export has no Emitter to log the fail-open case
// through, unlike Up.
func nodeContainers(ctx context.Context, rt cri.Runtime, allNodes []string, out plugin.Emitter) ([]plugin.ContainerInfo, error) {
	controlPlane, err := bootstrapControlPlaneNode(allNodes)
	if err != nil {
		return nil, fmt.Errorf("kind: capture: %w", err)
	}

	exclude, err := podAndServiceCIDRs(ctx, rt, controlPlane)
	if err != nil {
		// A verified exclusion list is what makes capture safe at all - not
		// finding one is a reason to skip capture for this cluster, not to
		// fail Up over it.
		if out != nil {
			out.Log("stdout", "skipping egress capture: "+err.Error())
		}
		return nil, nil
	}

	// A missing or unreadable set of friendly names is a UX degradation
	// (Name below falls back to the raw container name), never a reason
	// to fail Up or skip capture over - unlike a missing CIDR list, a
	// missing name carries no safety consequence.
	names := nodeNames(ctx, rt, controlPlane)

	containers := make([]plugin.ContainerInfo, 0, len(allNodes))
	for _, node := range allNodes {
		info, err := rt.Inspect(ctx, node)
		if err != nil {
			return nil, fmt.Errorf("kind: capture: inspect %s: %w", node, err)
		}
		if info.NetnsPath == "" {
			continue
		}
		containers = append(containers, containerInfoFor(node, info, exclude, names))
	}
	return containers, nil
}

// containerInfoFor builds one node's ContainerInfo. name takes node's
// kevin.node label value from names when it has one, the raw container
// name otherwise.
func containerInfoFor(node string, info cri.Container, exclude []string, names map[string]string) plugin.ContainerInfo {
	name := node
	if friendly, ok := names[node]; ok {
		name = friendly
	}
	return plugin.ContainerInfo{
		ID:           info.ID,
		Name:         name,
		NetnsPath:    info.NetnsPath,
		ExcludeCIDRs: exclude,
	}
}

// nodeNames reads back nodeLabelKey off every node in the cluster, keyed
// by the node's own name - which, in kind, is always identical to its
// docker container name, so this maps directly onto allNodes' own
// entries. Returns nil when the read fails: the cluster may not be fully
// up yet, or kubectl/the engine may be unreachable - a missing friendly
// name is a reason to fall back to the raw container name, not to fail
// the caller, so there is no error to report back.
func nodeNames(ctx context.Context, rt cri.Runtime, controlPlaneNode string) map[string]string {
	out, err := kubectl(ctx, rt, controlPlaneNode, "get", "nodes", "-o", "json")
	if err != nil {
		return nil
	}
	names, err := parseNodeLabels(out)
	if err != nil {
		return nil
	}
	return names
}

// parseNodeLabels extracts nodeLabelKey's value off each node in a
// "kubectl get nodes -o json" response, keyed by the node's own name.
func parseNodeLabels(nodesJSON string) (map[string]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(nodesJSON), &list); err != nil {
		return nil, fmt.Errorf("kind: parse node list: %w", err)
	}

	names := make(map[string]string, len(list.Items))
	for _, item := range list.Items {
		if name, ok := item.Metadata.Labels[nodeLabelKey]; ok {
			names[item.Metadata.Name] = name
		}
	}
	return names, nil
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
func podAndServiceCIDRs(ctx context.Context, rt cri.Runtime, controlPlaneNode string) ([]string, error) {
	out, err := kubectl(ctx, rt, controlPlaneNode, "-n", "kube-system", "get", "configmap", "kubeadm-config",
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
