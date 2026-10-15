package pluginindex

import (
	"fmt"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// InstallRequest asks [Install] to add a plugin version to a project.
type InstallRequest struct {
	// Plugin and Version are what to install.
	Plugin  Plugin
	Version Version

	// Dir and Name select the project environment file the plugins: entry is
	// added to.
	Dir  string
	Name string

	// NoTrust skips adding the version's signers to the local trust stores.
	NoTrust bool
}

// InstallResult is what [Install] did.
type InstallResult struct {
	// Trusted names each signer added to a trust store: a minisign key id, or
	// "identity/issuer" for a sigstore signer.
	Trusted []string

	// Warnings are problems that did not stop the install.
	Warnings []string
}

// Install trusts the signers the index declares for the version's signing
// scheme, then adds the version's plugins: entry to the environment file.
// An unsigned version trusts nothing. A signed version with no matching
// signer only warns: the plugins: entry is safe to add without trust.
//
// Install returns what it trusted before a failure along with the error.
func Install(req InstallRequest) (InstallResult, error) {
	var result InstallResult
	if !req.NoTrust {
		var err error
		if result, err = trustVersion(req.Plugin, req.Version); err != nil {
			return result, err
		}
	}

	snippet, err := Snippet(req.Version, req.Plugin.Name)
	if err != nil {
		return result, err
	}
	return result, config.InsertPlugin(req.Dir, req.Name, snippet)
}

// trustVersion trusts every signer of p whose scheme matches v's signing
// scheme.
func trustVersion(p Plugin, v Version) (InstallResult, error) {
	var result InstallResult
	sig := v.Source.Signing
	if sig == nil {
		return result, nil
	}
	signers := p.SignersFor(sig.Scheme)
	if len(signers) == 0 {
		result.Warnings = []string{fmt.Sprintf("no %s signer declared for %s", sig.Scheme, p.Name)}
		return result, nil
	}
	for _, s := range signers {
		id, err := trustSigner(s)
		if err != nil {
			return result, err
		}
		result.Trusted = append(result.Trusted, id)
	}
	return result, nil
}

// trustSigner adds s to the trust store of its scheme and returns what it
// trusted, for display.
func trustSigner(s Signer) (string, error) {
	switch s.Scheme {
	case config.SigningSchemeMinisign:
		return pkgtrust.AddKeyText(s.Key)
	case config.SigningSchemeSigstore:
		if err := pkgtrust.AddIdentity(s.Identity, s.Issuer); err != nil {
			return "", err
		}
		return s.Identity + "/" + s.Issuer, nil
	default:
		return "", fmt.Errorf("pluginindex: unknown signer scheme %q", s.Scheme)
	}
}
