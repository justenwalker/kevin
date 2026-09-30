package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/justenwalker/kevin/plugin"
)

// caAnchorPath is where TrustCA writes the kevin root certificate inside a
// node. update-ca-certificates reads every file in the directory.
const caAnchorPath = "/usr/local/share/ca-certificates/kevin-root.crt"

// containerdReadyTimeout bounds the wait for containerd after a restart.
const containerdReadyTimeout = 30 * time.Second

// containerdPollInterval is the wait between two checks of containerd.
const containerdPollInterval = 200 * time.Millisecond

// TrustCA writes the kevin root certificate into every node, reloads the
// trust store, and restarts containerd.
func (d *kindDriver) TrustCA(ctx context.Context, allNodes []string, caPEM string, out plugin.Emitter) error {
	out.Log("stdout", "installing the kevin root certificate into the nodes")
	out.Progress("trusting the kevin ca", 0, 0)

	for _, node := range allNodes {
		if err := d.trustCAOnNode(ctx, node, caPEM); err != nil {
			return err
		}
	}

	out.Log("stdout", "the nodes trust the kevin root certificate")
	return nil
}

// trustCAOnNode installs the certificate on one node container and waits
// for containerd to answer again.
func (d *kindDriver) trustCAOnNode(ctx context.Context, container, caPEM string) error {
	if _, err := d.rt.ExecInput(ctx, container, strings.NewReader(caPEM),
		"tee", caAnchorPath); err != nil {
		return fmt.Errorf("kubernetes: kind: write the kevin root certificate into %s: %w", container, err)
	}
	// update-ca-certificates warns about every file in the anchor directory
	// that holds more than one certificate, and a node carries such files.
	// Check the bundle rather than the exit code.
	_, _ = d.rt.Exec(ctx, container, "update-ca-certificates")

	if err := d.verifyTrusted(ctx, container, caPEM); err != nil {
		return err
	}
	if _, err := d.rt.Exec(ctx, container, "systemctl", "restart", "containerd"); err != nil {
		return fmt.Errorf("kubernetes: kind: restart containerd on %s: %w", container, err)
	}
	return d.waitContainerdReady(ctx, container)
}

// verifyTrusted reports whether the system bundle of a node holds the
// certificate. verifyTrusted returns [ErrNotTrusted] when the bundle does not.
func (d *kindDriver) verifyTrusted(ctx context.Context, container, caPEM string) error {
	bundle, err := d.rt.Exec(ctx, container, "cat", systemBundlePath)
	if err != nil {
		return fmt.Errorf("kubernetes: kind: read the trust store of %s: %w", container, err)
	}
	if !strings.Contains(normalizePEM(bundle), normalizePEM(caPEM)) {
		return fmt.Errorf("kubernetes: kind: %s: %w", container, ErrNotTrusted)
	}
	return nil
}

// waitContainerdReady polls a node until containerd answers again.
// waitContainerdReady returns ErrContainerdNotReady when the timeout passes
// first.
func (d *kindDriver) waitContainerdReady(ctx context.Context, container string) error {
	deadline := time.Now().Add(containerdReadyTimeout)
	for {
		if _, err := d.rt.Exec(ctx, container, "ctr", "version"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kubernetes: kind: %s: %w", container, ErrContainerdNotReady)
		}

		select {
		case <-time.After(containerdPollInterval):
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
	}
}

// PointDNSAtRelay rewrites every node's own /etc/resolv.conf to name relay
// as its only nameserver.
func (d *kindDriver) PointDNSAtRelay(ctx context.Context, allNodes []string, relay string) error {
	for _, node := range allNodes {
		if _, err := d.rt.Exec(ctx, node, "sh", "-c", "echo nameserver "+relay+" > /etc/resolv.conf"); err != nil {
			return fmt.Errorf("kubernetes: kind: point %s's dns at the relay: %w", node, err)
		}
	}
	return nil
}
