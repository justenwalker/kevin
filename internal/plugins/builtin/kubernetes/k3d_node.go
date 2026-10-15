package kubernetes

import (
	"context"

	"github.com/justenwalker/kevin/plugin"
)

// TrustCA checks that every node holds the kevin root certificate. Create
// mounted it into the nodes before k3s started, and k3s reads the directory
// once, at start, so there is nothing to install afterward.
func (d *k3dDriver) TrustCA(ctx context.Context, allNodes []string, caPEM string, out plugin.Emitter) error {
	out.Log("stdout", "checking that the nodes hold the kevin root certificate")
	out.Progress("trusting the kevin ca", 0, 0)

	if err := d.nodes().verifyFileHolds(ctx, allNodes, k3dCAPath, caPEM); err != nil {
		return err
	}

	out.Log("stdout", "the nodes trust the kevin root certificate")
	return nil
}

// PointDNSAtRelay rewrites every node's own /etc/resolv.conf to name relay
// as its only nameserver.
func (d *k3dDriver) PointDNSAtRelay(ctx context.Context, allNodes []string, relay string) error {
	return d.nodes().PointDNSAtRelay(ctx, allNodes, relay)
}
