package kind

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/justenwalker/kevin/plugin"
)

// systemBundlePath is the file that update-ca-certificates writes. A node
// trusts a certificate when the bundle holds it.
const systemBundlePath = "/etc/ssl/certs/ca-certificates.crt"

// caAnchorPath is where installTrustCA writes the kevin root certificate
// inside a node. update-ca-certificates reads every file in the directory.
const caAnchorPath = "/usr/local/share/ca-certificates/kevin-root.crt"

// containerdReadyTimeout bounds the wait for containerd after a restart.
const containerdReadyTimeout = 30 * time.Second

// containerdPollInterval is the wait between two checks of containerd.
const containerdPollInterval = 200 * time.Millisecond

// wantsTrustCA reports whether Up must install the kevin root certificate
// into the nodes. An environment with no certificate authority needs no
// install.
func wantsTrustCA(cfg config, env plugin.Env) bool {
	return cfg.TrustCA && env.CAPath != ""
}

// trustCAFromPath reads the kevin root certificate from the host path that
// plugin.Env.CAPath names, then installs it into every node.
func trustCAFromPath(ctx context.Context, allNodes []string, caPath string, out plugin.Emitter) error {
	caPEM, err := os.ReadFile(caPath) //nolint:gosec // caPath is plugin.Env.CAPath, set by the supervisor, not user input
	if err != nil {
		return fmt.Errorf("kind: read the kevin root certificate: %w", err)
	}
	return installTrustCA(ctx, allNodes, string(caPEM), out)
}

// installTrustCA writes the kevin root certificate into every node, reloads
// the trust store, and restarts containerd.
func installTrustCA(ctx context.Context, allNodes []string, caPEM string, out plugin.Emitter) error {
	out.Log("stdout", "installing the kevin root certificate into the nodes")
	out.Progress("trusting the kevin ca", 0, 0)

	for _, node := range allNodes {
		if err := installTrustCAOnNode(ctx, node, caPEM); err != nil {
			return err
		}
	}

	out.Log("stdout", "the nodes trust the kevin root certificate")
	return nil
}

// installTrustCAOnNode installs the certificate on one node container and
// waits for containerd to answer again.
func installTrustCAOnNode(ctx context.Context, container, caPEM string) error {
	if _, err := dockerClient.ExecInput(ctx, container, strings.NewReader(caPEM),
		"tee", caAnchorPath); err != nil {
		return fmt.Errorf("kind: write the kevin root certificate into %s: %w", container, err)
	}
	// update-ca-certificates warns about every file in the anchor directory
	// that holds more than one certificate, and a node carries such files.
	// Check the bundle rather than the exit code.
	_, _ = dockerClient.Exec(ctx, container, "update-ca-certificates")

	if err := verifyTrusted(ctx, container, caPEM); err != nil {
		return err
	}
	if _, err := dockerClient.Exec(ctx, container, "systemctl", "restart", "containerd"); err != nil {
		return fmt.Errorf("kind: restart containerd on %s: %w", container, err)
	}
	if err := waitContainerdReady(ctx, container); err != nil {
		return err
	}
	return nil
}

// verifyTrusted reports whether the system bundle of a node holds the
// certificate. verifyTrusted returns [ErrNotTrusted] when the bundle does not.
func verifyTrusted(ctx context.Context, container, caPEM string) error {
	bundle, err := dockerClient.Exec(ctx, container, "cat", systemBundlePath)
	if err != nil {
		return fmt.Errorf("kind: read the trust store of %s: %w", container, err)
	}
	if !strings.Contains(normalizePEM(bundle), normalizePEM(caPEM)) {
		return fmt.Errorf("kind: %s: %w", container, ErrNotTrusted)
	}
	return nil
}

// normalizePEM strips the line endings of a PEM block.
func normalizePEM(pem string) string {
	return strings.ReplaceAll(strings.TrimSpace(pem), "\r\n", "\n")
}

// waitContainerdReady polls a node until containerd answers again.
// waitContainerdReady returns ErrContainerdNotReady when the timeout passes
// first.
func waitContainerdReady(ctx context.Context, container string) error {
	deadline := time.Now().Add(containerdReadyTimeout)
	for {
		if _, err := dockerClient.Exec(ctx, container, "ctr", "version"); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("kind: %s: %w", container, ErrContainerdNotReady)
		}

		select {
		case <-time.After(containerdPollInterval):
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
	}
}
