package trust

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keychain is the macOS trust store.
//
// The user domain needs no root, but macOS still asks the user to confirm
// the change to the trust settings - a headless run of the command fails.
type keychain struct{}

// SecurityBinary is the macOS command that edits a keychain.
const SecurityBinary = "/usr/bin/security"

// systemKeychain holds the machine-wide trust settings.
const systemKeychain = "/Library/Keychains/System.keychain"

// loginKeychain returns the keychain of the user.
func loginKeychain() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("trust: find the home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db"), nil
}

// installArgs builds the arguments that add an authority to a keychain.
func (keychain) installArgs(req Request, path string) []string {
	args := []string{"add-trusted-cert"}
	if req.System {
		// -d writes the admin domain, which is the machine-wide store.
		args = append(args, "-d")
	}
	return append(args, "-r", "trustRoot", "-k", path, req.CertPath)
}

// removeArgs builds the arguments that delete an authority from a keychain.
func (keychain) removeArgs(req Request, path string) []string {
	return []string{"delete-certificate", "-c", req.CommonName, path}
}

func (k keychain) target(req Request) (string, error) {
	if req.System {
		return systemKeychain, nil
	}
	return loginKeychain()
}

func (k keychain) name(req Request) string {
	if req.System {
		return "macos-system"
	}
	return "macos-user"
}

func (k keychain) install(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: k.name(req)}

	path, err := k.target(req)
	if err != nil {
		return result, err
	}
	args := k.installArgs(req, path)

	if req.System && !isRoot() {
		result.Reason = "run: sudo " + quote(SecurityBinary, args...)
		return result, fmt.Errorf("trust: the system keychain: %w", ErrNeedsRoot)
	}

	if _, err = runCmd(ctx, SecurityBinary, args...); err != nil {
		result.Reason = "run: " + quote(SecurityBinary, args...)
		return result, err
	}

	result.Installed = true
	return result, nil
}

func (k keychain) remove(ctx context.Context, req Request) (Result, error) {
	result := Result{Store: k.name(req)}

	path, err := k.target(req)
	if err != nil {
		return result, err
	}
	args := k.removeArgs(req, path)

	if req.System && !isRoot() {
		result.Reason = "run: sudo " + quote(SecurityBinary, args...)
		return result, fmt.Errorf("trust: the system keychain: %w", ErrNeedsRoot)
	}

	out, err := runCmd(ctx, SecurityBinary, args...)
	if err != nil {
		// A removal must be idempotent. security reports an absent
		// certificate on standard error and exits non-zero.
		if absentFromKeychain(out) {
			result.Skipped = true
			result.Reason = "the keychain does not hold the authority"
			return result, nil
		}
		result.Reason = "run: " + quote(SecurityBinary, args...)
		return result, err
	}

	result.Installed = false
	return result, nil
}

// absentFromKeychain reports whether the output of security says that the
// keychain holds no such certificate.
func absentFromKeychain(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "unable to delete certificate matching") ||
		strings.Contains(lower, "could not be found")
}
