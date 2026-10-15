package load

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jedisct1/go-minisign"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/cosign"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/plugins/signing"
	"github.com/justenwalker/kevin/internal/plugins/source/cache"
	"github.com/justenwalker/kevin/internal/uerr"
)

// verifySource checks pkg against the detached signature src ships with it,
// by signing.Scheme and the local trust stores. It is a no-op when signing
// is nil.
func verifySource(ctx context.Context, src source, pkg cache.Entry, signing *config.SigningSpec, cosignRunner command.Runner) error {
	if signing == nil {
		return nil
	}
	sig, err := src.signature(ctx, pkg, signing.Scheme)
	if err != nil {
		return err
	}
	if signing.Scheme == config.SigningSchemeSigstore {
		return verifySigstoreBlobBytes(ctx, pkg.Path, sig, signing, cosignRunner)
	}
	return verifyPackageSignature(pkg.Path, sig)
}

// verifyPackageSignature parses sigData as a minisign signature and checks
// it against pkgPath's bytes and the local trust store
// (~/.kevin/trusted-keys, see internal/plugins/signing).
func verifyPackageSignature(pkgPath string, sigData []byte) error {
	sig, err := minisign.DecodeSignature(string(sigData))
	if err != nil {
		return fmt.Errorf("%w: %w", signing.ErrSignatureInvalid, err)
	}
	keyring, err := signing.Load()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(pkgPath) //nolint:gosec // pkgPath is a fetched/cached package path, not user input
	if err != nil {
		return fmt.Errorf("read %q: %w", pkgPath, err)
	}
	return keyring.Verify(sig, data)
}

// verifySigstoreBlob checks pkgPath's bytes against bundlePath (a `cosign
// sign-blob --bundle` output) for spec.Identity/spec.Issuer, gated by
// the local identity trust store (~/.kevin/trusted-identities, see
// signing.VerifyIdentity).
func verifySigstoreBlob(ctx context.Context, pkgPath, bundlePath string, spec *config.SigningSpec, cosignRunner command.Runner) error {
	// kevin.cue's own identity/issuer isn't the trust boundary - see
	// signing.VerifyIdentity for why - so that check runs first.
	if err := signing.VerifyIdentity(spec.Identity, spec.Issuer); err != nil {
		return err
	}
	return cosign.New(cosignRunner).VerifyBlob(ctx, pkgPath, bundlePath, spec.Identity, spec.Issuer)
}

// verifySigstoreBlobBytes is [verifySigstoreBlob] for a bundle fetched into
// memory (an http: or oci: source) rather than already sitting on disk next
// to a file: package - cosign verify-blob takes a bundle path, not stdin,
// so this writes bundleData to a temp file first.
func verifySigstoreBlobBytes(ctx context.Context, pkgPath string, bundleData []byte, signing *config.SigningSpec, cosignRunner command.Runner) error {
	tmp, err := os.CreateTemp("", "kevin-sigstore-bundle-*.json")
	if err != nil {
		return fmt.Errorf("create temp bundle file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup, the temp dir gets reaped regardless

	if _, err := tmp.Write(bundleData); err != nil {
		tmp.Close() //nolint:errcheck,gosec // best effort; the write error below is what's reported
		return fmt.Errorf("write temp bundle file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp bundle file: %w", err)
	}
	return verifySigstoreBlob(ctx, pkgPath, tmp.Name(), signing, cosignRunner)
}

// friendlySignatureErr attaches a human-facing message to err when it names
// a signature-verification failure signing or sigstorepkg recognizes, so
// plugins.<name>'s user sees what to do next instead of a raw sentinel. It
// returns err unchanged for anything else.
func friendlySignatureErr(err error, name string) error {
	switch {
	case errors.Is(err, signing.ErrSignatureMissing):
		return uerr.Wrap(err, "plugins.%s has a signing block but ships no signature file - remove signing, or add the signature", name)
	case errors.Is(err, signing.ErrUnknownKeyID):
		return uerr.Wrap(err, "plugins.%s's signature key isn't trusted - run `kevin plugin trust add <keyfile>` first", name)
	case errors.Is(err, signing.ErrIdentityUntrusted):
		return uerr.Wrap(err, "plugins.%s's signing identity isn't trusted - run `kevin plugin trust add-identity` first", name)
	case errors.Is(err, signing.ErrSignatureInvalid):
		return uerr.Wrap(err, "plugins.%s's signature doesn't verify against its package - it may be corrupted or tampered with", name)
	case errors.Is(err, cosign.ErrVerifyFailed):
		return uerr.Wrap(err, "plugins.%s's sigstore signature doesn't verify against its package - it may be corrupted or tampered with", name)
	case errors.Is(err, cosign.ErrCosignNotFound):
		return uerr.Wrap(err, "plugins.%s needs cosign to verify its sigstore signature - install it: https://docs.sigstore.dev/cosign/system_config/installation/", name)
	default:
		return err
	}
}
