package kubernetes

import (
	"context"
	"fmt"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

// TrustCA checks that every node holds the kevin root certificate. Create
// mounted it into the nodes before k3s started, and k3s reads the directory
// once, at start, so there is nothing to install afterward.
func (d *k3dDriver) TrustCA(ctx context.Context, allNodes []string, caPEM string, out plugin.Emitter) error {
	out.Log("stdout", "checking that the nodes hold the kevin root certificate")
	out.Progress("trusting the kevin ca", 0, 0)

	for _, node := range allNodes {
		mounted, err := d.rt.Exec(ctx, node, "cat", k3dCAPath)
		if err != nil {
			return fmt.Errorf("kubernetes: k3d: read the kevin root certificate on %s: %w", node, err)
		}
		if !strings.Contains(normalizePEM(mounted), normalizePEM(caPEM)) {
			return fmt.Errorf("kubernetes: k3d: %s: %w", node, ErrNotTrusted)
		}
	}

	out.Log("stdout", "the nodes trust the kevin root certificate")
	return nil
}

// PointDNSAtRelay rewrites every node's own /etc/resolv.conf to name relay
// as its only nameserver.
func (d *k3dDriver) PointDNSAtRelay(ctx context.Context, allNodes []string, relay string) error {
	for _, node := range allNodes {
		if _, err := d.rt.Exec(ctx, node, "sh", "-c", "echo nameserver "+relay+" > /etc/resolv.conf"); err != nil {
			return fmt.Errorf("kubernetes: k3d: point %s's dns at the relay: %w", node, err)
		}
	}
	return nil
}
