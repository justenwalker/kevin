package kubernetes

import (
	"context"
	"fmt"

	"github.com/justenwalker/kevin/internal/cri"
)

// joinProjectNetwork connects every node to the project network, which then
// carries the default route of the node, and checks that each node is on it.
// A node keeps the network its cluster tool created for it. An empty network
// skips the step: no network was asked for.
func joinProjectNetwork(ctx context.Context, rt cri.Runtime, nodes []string, network string) error {
	if network == "" {
		return nil
	}
	for _, node := range nodes {
		if err := rt.NetworkConnect(ctx, network, node); err != nil {
			return fmt.Errorf("kubernetes: connect %s to %s: %w", node, network, err)
		}
		if err := verifyNetworkMembership(ctx, rt, node, network); err != nil {
			return err
		}
	}
	return nil
}

// verifyNetworkMembership returns [ErrNetworkMismatch] when container is not
// on network.
func verifyNetworkMembership(ctx context.Context, rt cri.Runtime, container, network string) error {
	info, err := rt.Inspect(ctx, container)
	if err != nil {
		return fmt.Errorf("kubernetes: inspect %s: %w", container, err)
	}
	if _, ok := info.IPs[network]; !ok {
		return fmt.Errorf("kubernetes: %s: %w: %q", container, ErrNetworkMismatch, network)
	}
	return nil
}
