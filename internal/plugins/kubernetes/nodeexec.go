package kubernetes

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
)

// nodeExec runs commands inside the node containers of one cluster. A driver
// builds one from its runtime, and the cluster tool it drives supplies only
// how kubectl is invoked inside a node.
type nodeExec struct {
	rt cri.Execer

	// driver labels errors with the driver's name.
	driver string

	controlPlane string

	// kubectlArgv wraps kubectl arguments in the command that runs kubectl
	// inside the control-plane node.
	kubectlArgv func(args []string) []string
}

// Kubectl runs kubectl inside the control-plane node.
func (n nodeExec) Kubectl(ctx context.Context, args ...string) (string, error) {
	if n.rt == nil {
		return "", fmt.Errorf("kubernetes: %s: kubectl: %w", n.driver, ErrNoRuntime)
	}
	out, err := n.rt.Exec(ctx, n.controlPlane, n.kubectlArgv(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: %s: kubectl: %w", n.driver, err)
	}
	return out, nil
}

// KubectlInput runs kubectl inside the control-plane node, with stdin feeding
// the command.
func (n nodeExec) KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if n.rt == nil {
		return "", fmt.Errorf("kubernetes: %s: kubectl: %w", n.driver, ErrNoRuntime)
	}
	out, err := n.rt.ExecInput(ctx, n.controlPlane, stdin, n.kubectlArgv(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: %s: kubectl: %w", n.driver, err)
	}
	return out, nil
}

// PointDNSAtRelay rewrites every node's own /etc/resolv.conf to name relay as
// its only nameserver.
func (n nodeExec) PointDNSAtRelay(ctx context.Context, nodes []string, relay string) error {
	for _, node := range nodes {
		if _, err := n.rt.Exec(ctx, node, "sh", "-c", "echo nameserver "+relay+" > /etc/resolv.conf"); err != nil {
			return fmt.Errorf("kubernetes: %s: point %s's dns at the relay: %w", n.driver, node, err)
		}
	}
	return nil
}

// verifyFileHolds checks that the file at path on every node holds pem. It
// returns [ErrNotTrusted] for the first node whose file does not.
func (n nodeExec) verifyFileHolds(ctx context.Context, nodes []string, path, pem string) error {
	for _, node := range nodes {
		got, err := n.rt.Exec(ctx, node, "cat", path)
		if err != nil {
			return fmt.Errorf("kubernetes: %s: read the kevin root certificate on %s: %w", n.driver, node, err)
		}
		if !strings.Contains(normalizePEM(got), normalizePEM(pem)) {
			return fmt.Errorf("kubernetes: %s: %s: %w", n.driver, node, ErrNotTrusted)
		}
	}
	return nil
}
