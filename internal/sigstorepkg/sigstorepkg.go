// Package sigstorepkg verifies a kevin plugin package's sigstore (cosign)
// keyless signature by shelling out to the cosign CLI - see ADR-0005 (shell
// out to CLIs, never embed sigstore-go/cosign's own Go libraries).
package sigstorepkg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Binary is the command this package runs.
const Binary = "cosign"

// LookPath resolves the cosign binary on PATH.
func LookPath() (string, error) {
	path, err := exec.LookPath(Binary)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrCosignNotFound, err)
	}
	return path, nil
}

// VerifyBlob checks pkgPath's bytes against bundlePath - a `cosign
// sign-blob --bundle` output - for a Fulcio certificate matching identity
// and issuer. The bundle carries its own Rekor inclusion proof, checked
// offline: this call makes no live Rekor request, only (at most) a
// Sigstore TUF trust-root refresh, cached under ~/.sigstore/root/.
func VerifyBlob(ctx context.Context, pkgPath, bundlePath, identity, issuer string) error {
	if _, err := LookPath(); err != nil {
		return err
	}

	//nolint:gosec // every argument is a configured path or kevin.cue identity/issuer, not a shell string
	cmd := exec.CommandContext(ctx, Binary, verifyBlobArgs(pkgPath, bundlePath, identity, issuer)...)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return verifyErr(err, stderr.String())
	}
	return nil
}

// verifyBlobArgs builds `cosign verify-blob`'s argument list.
func verifyBlobArgs(pkgPath, bundlePath, identity, issuer string) []string {
	return []string{
		"verify-blob",
		"--bundle", bundlePath,
		"--certificate-identity", identity,
		"--certificate-oidc-issuer", issuer,
		pkgPath,
	}
}

// verifyErr wraps a failed `cosign verify-blob` run as [ErrVerifyFailed],
// attaching cosign's own stderr when it printed one.
func verifyErr(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		return fmt.Errorf("%w: %w", ErrVerifyFailed, err)
	}
	return fmt.Errorf("%w: %s", ErrVerifyFailed, msg)
}
