package kubernetes

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/justenwalker/kevin/plugin"
)

// systemBundlePath is the file that update-ca-certificates writes inside a
// node. A node trusts a certificate when the bundle holds it.
const systemBundlePath = "/etc/ssl/certs/ca-certificates.crt"

// wantsTrustCA reports whether Up must install the kevin root certificate
// into the nodes. An environment with no certificate authority needs no
// install.
func wantsTrustCA(cfg config, env plugin.Env) bool {
	return cfg.TrustCA && env.CAPath != ""
}

// trustCAFromPath reads the kevin root certificate from the host path that
// plugin.Env.CAPath names, then has the driver install it into every node.
func trustCAFromPath(ctx context.Context, drv driver, allNodes []string, caPath string, out plugin.Emitter) error {
	caPEM, err := os.ReadFile(caPath) //nolint:gosec // caPath is plugin.Env.CAPath, set by the supervisor, not user input
	if err != nil {
		return fmt.Errorf("kubernetes: read the kevin root certificate: %w", err)
	}
	return drv.TrustCA(ctx, allNodes, string(caPEM), out)
}

// normalizePEM strips the line endings of a PEM block.
func normalizePEM(pem string) string {
	return strings.ReplaceAll(strings.TrimSpace(pem), "\r\n", "\n")
}
