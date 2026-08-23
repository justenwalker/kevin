package kind

import (
	"fmt"
	"sort"
	"strings"
)

// bootstrapControlPlaneNode returns the container name of the cluster's
// bootstrap control-plane node - the one CoreDNS patching and the relay pod
// run against. kind names its first control-plane node "<cluster>-
// control-plane" and any additional ones (a hand-written HA config) "
// <cluster>-control-planeN" - sorting the matching names picks that first
// one, the same selection kind's own nodeutils.BootstrapControlPlaneNode
// makes by sorting node names and taking the first.
func bootstrapControlPlaneNode(names []string) (string, error) {
	var controlPlanes []string
	for _, name := range names {
		if strings.Contains(name, "-control-plane") {
			controlPlanes = append(controlPlanes, name)
		}
	}
	if len(controlPlanes) == 0 {
		return "", fmt.Errorf("kind: %w", ErrNoControlPlaneNode)
	}
	sort.Strings(controlPlanes)
	return controlPlanes[0], nil
}
