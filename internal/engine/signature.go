package engine

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jedisct1/go-minisign"

	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pkgtrust"
	"github.com/justenwalker/kevin/internal/uerr"
)

// verifyFileSignature checks pkgPath's sibling pkgPath+".minisig" -
// minisign's own default suffix for `minisign -S`/`-Sm` - against the local
// trust store. It is a no-op when signed is false.
func verifyFileSignature(pkgPath string, signed bool) error {
	if !signed {
		return nil
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
// against the local trust store. It is a no-op when signed is false.
func verifyHTTPSignature(ctx context.Context, rawURL, pkgPath string, signed bool) error {
	if !signed {
		return nil
	}
	data, err := httppkg.FetchSignature(ctx, rawURL)
	if err != nil {
		return err
	}
	return verifyPackageSignature(pkgPath, data)
}

// verifyOCISignature fetches ref's detached signature - published at the
// cosign-style fallback tag pkgDigest names - and checks it against the
// local trust store. It is a no-op when signed is false.
func verifyOCISignature(ctx context.Context, ref, pkgDigest, pkgPath string, signed bool) error {
	if !signed {
		return nil
	}
	data, err := ocipkg.FetchSignature(ctx, ref, pkgDigest)
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

// friendlySignatureErr attaches a human-facing message to err when it names
// a signature-verification failure pkgtrust recognizes, so plugins.<name>'s
// user sees what to do next instead of a raw sentinel. It returns err
// unchanged for anything else.
func friendlySignatureErr(err error, name string) error {
	switch {
	case errors.Is(err, pkgtrust.ErrSignatureMissing):
		return uerr.Wrap(err, "plugins.%s is marked signed: true but ships no .minisig signature - remove signed: true, or add the signature", name)
	case errors.Is(err, pkgtrust.ErrUnknownKeyID):
		return uerr.Wrap(err, "plugins.%s's signature key isn't trusted - run `kevin plugin trust add <keyfile>` first", name)
	case errors.Is(err, pkgtrust.ErrSignatureInvalid):
		return uerr.Wrap(err, "plugins.%s's signature doesn't verify against its package - it may be corrupted or tampered with", name)
	default:
		return err
	}
}
