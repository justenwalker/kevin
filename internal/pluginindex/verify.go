package pluginindex

import (
	"context"
	"fmt"
	"os"

	"github.com/jedisct1/go-minisign"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/cosign"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// verifyVersionFile checks path against whichever detached signature
// sibling exists next to it - path+".minisig" or path+".sigstore.json" -
// using only signers' own matching-scheme entries, never the global
// trust store. Neither sibling existing is [ErrVersionSignatureMissing];
// either existing but not verifying is [ErrVersionSignatureInvalid].
func verifyVersionFile(ctx context.Context, path string, signers []Signer) error {
	if _, err := os.Stat(path + ".minisig"); err == nil {
		return verifyMinisignFile(path, path+".minisig", signers)
	}
	if _, err := os.Stat(path + ".sigstore.json"); err == nil {
		return verifySigstoreFile(ctx, path, path+".sigstore.json", signers)
	}
	return fmt.Errorf("pluginindex: %q: %w", path, ErrVersionSignatureMissing)
}

// verifyMinisignFile checks path's bytes against sigPath's detached
// minisign signature, using an ephemeral keyring built from signers' own
// minisign entries.
func verifyMinisignFile(path, sigPath string, signers []Signer) error {
	sigData, err := os.ReadFile(sigPath) //nolint:gosec // path/sigPath come from a locally cloned index repo, not user input
	if err != nil {
		return fmt.Errorf("pluginindex: read %q: %w", sigPath, err)
	}
	sig, err := minisign.DecodeSignature(string(sigData))
	if err != nil {
		return fmt.Errorf("pluginindex: %q: %w: %w", sigPath, ErrVersionSignatureInvalid, err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path comes from a locally cloned index repo, not user input
	if err != nil {
		return fmt.Errorf("pluginindex: read %q: %w", path, err)
	}
	if err := minisignKeyring(signers).Verify(sig, data); err != nil {
		return fmt.Errorf("pluginindex: %q: %w: %w", path, ErrVersionSignatureInvalid, err)
	}
	return nil
}

// minisignKeyring builds an ephemeral pkgtrust.Keyring from signers' own
// minisign entries only - never pkgtrust.Load's global trust store, so a
// federated version source is checked only against this plugin's own
// declared signers.
func minisignKeyring(signers []Signer) pkgtrust.Keyring {
	kr := make(pkgtrust.Keyring)
	for _, s := range signers {
		if s.Scheme != config.SigningSchemeMinisign {
			continue
		}
		pub, err := minisign.DecodePublicKey(s.Key)
		if err != nil {
			continue // already validated at plugin.yaml load time (loadPluginMeta)
		}
		kr[pub.KeyId] = pub
	}
	return kr
}

// verifySigstoreFile tries path against bundlePath for each of signers'
// own sigstore identity/issuer pairs (cosign.Client.VerifyBlob, the same
// call internal/engine/signature.go makes for package verification),
// succeeding on the first match. path is already a real file on disk
// (this plugin's own cloned version source), so unlike package
// verification of an in-memory HTTP/OCI fetch, no temp file is needed.
func verifySigstoreFile(ctx context.Context, path, bundlePath string, signers []Signer) error {
	var lastErr error
	for _, s := range signers {
		if s.Scheme != config.SigningSchemeSigstore {
			continue
		}
		err := cosign.New(command.Default).VerifyBlob(ctx, path, bundlePath, s.Identity, s.Issuer)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrVersionSignatureMissing
	}
	return fmt.Errorf("pluginindex: %q: %w: %w", path, ErrVersionSignatureInvalid, lastErr)
}
