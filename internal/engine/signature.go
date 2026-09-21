package engine

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jedisct1/go-minisign"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pkgtrust"
	"github.com/justenwalker/kevin/internal/sigstorepkg"
	"github.com/justenwalker/kevin/internal/uerr"
)

// verifyFileSignature checks pkgPath's sibling signature file against the
// local trust store, dispatched by signing.Scheme. It is a no-op when
// signing is nil. The minisign scheme reads pkgPath+".minisig" - minisign's
// own default suffix for `minisign -S`/`-Sm`. The sigstore scheme reads
// pkgPath+".sigstore.json" - `cosign sign-blob --bundle`'s own default
// naming.
func verifyFileSignature(ctx context.Context, pkgPath string, signing *config.SigningSpec) error {
	if signing == nil {
		return nil
	}
	if signing.Scheme == config.SigningSchemeSigstore {
		bundlePath := pkgPath + ".sigstore.json"
		if _, err := os.Stat(bundlePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("%q: %w", bundlePath, pkgtrust.ErrSignatureMissing)
			}
			return fmt.Errorf("stat %q: %w", bundlePath, err)
		}
		return verifySigstoreBlob(ctx, pkgPath, bundlePath, signing)
	}
	sigPath := pkgPath + ".minisig"
	data, err := os.ReadFile(sigPath) //nolint:gosec // pkgPath is a configured plugin package path, the whole point
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%q: %w", sigPath, pkgtrust.ErrSignatureMissing)
	}
	if err != nil {
		return fmt.Errorf("read %q: %w", sigPath, err)
	}
	return verifyPackageSignature(pkgPath, data)
}

// verifyHTTPSignature fetches rawURL's detached signature and checks it
// against the local trust store, dispatched by signing.Scheme. It is a
// no-op when signing is nil.
func verifyHTTPSignature(ctx context.Context, rawURL, pkgPath string, signing *config.SigningSpec) error {
	if signing == nil {
		return nil
	}
	if signing.Scheme == config.SigningSchemeSigstore {
		data, err := httppkg.FetchSignature(ctx, rawURL, ".sigstore.json")
		if err != nil {
			return err
		}
		return verifySigstoreBlobBytes(ctx, pkgPath, data, signing)
	}
	data, err := httppkg.FetchSignature(ctx, rawURL, ".minisig")
	if err != nil {
		return err
	}
	return verifyPackageSignature(pkgPath, data)
}

// verifyOCISignature fetches ref's detached signature - published at the
// cosign-style fallback tag pkgDigest names - and checks it against the
// local trust store, dispatched by signing.Scheme. It is a no-op when
// signing is nil.
func verifyOCISignature(ctx context.Context, ref, pkgDigest, pkgPath string, signing *config.SigningSpec) error {
	if signing == nil {
		return nil
	}
	if signing.Scheme == config.SigningSchemeSigstore {
		data, err := ocipkg.FetchSignature(ctx, ref, pkgDigest, ocipkg.SigstoreSignatureMediaType)
		if err != nil {
			return err
		}
		return verifySigstoreBlobBytes(ctx, pkgPath, data, signing)
	}
	data, err := ocipkg.FetchSignature(ctx, ref, pkgDigest, ocipkg.SignatureMediaType)
	if err != nil {
		return err
	}
	return verifyPackageSignature(pkgPath, data)
}

// verifyPackageSignature parses sigData as a minisign signature and checks
// it against pkgPath's bytes and the local trust store
// (~/.kevin/trusted-keys, see internal/pkgtrust).
func verifyPackageSignature(pkgPath string, sigData []byte) error {
	sig, err := minisign.DecodeSignature(string(sigData))
	if err != nil {
		return fmt.Errorf("%w: %w", pkgtrust.ErrSignatureInvalid, err)
	}
	keyring, err := pkgtrust.Load()
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
// sign-blob --bundle` output) for signing.Identity/signing.Issuer, gated by
// the local identity trust store (~/.kevin/trusted-identities, see
// pkgtrust.VerifyIdentity).
func verifySigstoreBlob(ctx context.Context, pkgPath, bundlePath string, signing *config.SigningSpec) error {
	// kevin.cue's own identity/issuer isn't the trust boundary - see
	// pkgtrust.VerifyIdentity for why - so that check runs first.
	if err := pkgtrust.VerifyIdentity(signing.Identity, signing.Issuer); err != nil {
		return err
	}
	return sigstorepkg.VerifyBlob(ctx, pkgPath, bundlePath, signing.Identity, signing.Issuer)
}

// verifySigstoreBlobBytes is [verifySigstoreBlob] for a bundle fetched into
// memory (an http: or oci: source) rather than already sitting on disk next
// to a file: package - cosign verify-blob takes a bundle path, not stdin,
// so this writes bundleData to a temp file first.
func verifySigstoreBlobBytes(ctx context.Context, pkgPath string, bundleData []byte, signing *config.SigningSpec) error {
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
	return verifySigstoreBlob(ctx, pkgPath, tmp.Name(), signing)
}

// friendlySignatureErr attaches a human-facing message to err when it names
// a signature-verification failure pkgtrust or sigstorepkg recognizes, so
// plugins.<name>'s user sees what to do next instead of a raw sentinel. It
// returns err unchanged for anything else.
func friendlySignatureErr(err error, name string) error {
	switch {
	case errors.Is(err, pkgtrust.ErrSignatureMissing):
		return uerr.Wrap(err, "plugins.%s has a signing block but ships no signature file - remove signing, or add the signature", name)
	case errors.Is(err, pkgtrust.ErrUnknownKeyID):
		return uerr.Wrap(err, "plugins.%s's signature key isn't trusted - run `kevin plugin trust add <keyfile>` first", name)
	case errors.Is(err, pkgtrust.ErrIdentityUntrusted):
		return uerr.Wrap(err, "plugins.%s's signing identity isn't trusted - run `kevin plugin trust add-identity` first", name)
	case errors.Is(err, pkgtrust.ErrSignatureInvalid):
		return uerr.Wrap(err, "plugins.%s's signature doesn't verify against its package - it may be corrupted or tampered with", name)
	case errors.Is(err, sigstorepkg.ErrVerifyFailed):
		return uerr.Wrap(err, "plugins.%s's sigstore signature doesn't verify against its package - it may be corrupted or tampered with", name)
	case errors.Is(err, sigstorepkg.ErrCosignNotFound):
		return uerr.Wrap(err, "plugins.%s needs cosign to verify its sigstore signature - install it: https://docs.sigstore.dev/cosign/system_config/installation/", name)
	default:
		return err
	}
}
