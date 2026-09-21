package pkgtrust

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// IdentityDir is ~/.kevin/trusted-identities - global, shared across every
// project, and separate from [Dir].
func IdentityDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kevin", "trusted-identities")
	}
	return filepath.Join(home, ".kevin", "trusted-identities")
}

// identityRecord is one trusted identity/issuer pair, as stored on disk.
type identityRecord struct {
	Identity string `json:"identity"`
	Issuer   string `json:"issuer"`
}

// identityFile derives the content-addressed file name identity/issuer
// stores under.
func identityFile(identity, issuer string) string {
	sum := sha256.Sum256([]byte(identity + "\x00" + issuer))
	return hex.EncodeToString(sum[:]) + ".json"
}

// AddIdentity records identity/issuer as trusted. Adding a pair already in
// the store is a no-op.
func AddIdentity(identity, issuer string) error {
	if err := os.MkdirAll(IdentityDir(), 0o700); err != nil {
		return fmt.Errorf("pkgtrust: create %q: %w", IdentityDir(), err)
	}
	data, err := json.Marshal(identityRecord{Identity: identity, Issuer: issuer})
	if err != nil {
		return fmt.Errorf("pkgtrust: encode identity: %w", err)
	}
	path := filepath.Join(IdentityDir(), identityFile(identity, issuer))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("pkgtrust: write %q: %w", path, err)
	}
	return nil
}

// IdentityInfo is one trusted sigstore identity, for [ListIdentities].
type IdentityInfo struct {
	Identity string
	Issuer   string
}

// ListIdentities reports every trusted identity/issuer pair. A Dir that
// does not exist yet reports no identities, not an error. A file that
// doesn't parse as an identity record is skipped, not an error - the
// trust store may hold stray files a user dropped in by hand.
func ListIdentities() ([]IdentityInfo, error) {
	entries, err := os.ReadDir(IdentityDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pkgtrust: read %q: %w", IdentityDir(), err)
	}
	out := make([]IdentityInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rec, err := readIdentityRecord(filepath.Join(IdentityDir(), e.Name()))
		if err != nil {
			continue
		}
		out = append(out, IdentityInfo(rec))
	}
	return out, nil
}

// readIdentityRecord reads and decodes one identity record file.
func readIdentityRecord(path string) (identityRecord, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is built from ListIdentities' own directory listing
	if err != nil {
		return identityRecord{}, fmt.Errorf("pkgtrust: read %q: %w", path, err)
	}
	var rec identityRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return identityRecord{}, fmt.Errorf("pkgtrust: decode %q: %w", path, err)
	}
	return rec, nil
}

// RemoveIdentity deletes the identity/issuer pair from the trust store.
func RemoveIdentity(identity, issuer string) error {
	path := filepath.Join(IdentityDir(), identityFile(identity, issuer))
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pkgtrust: %q/%q: %w", identity, issuer, ErrIdentityUntrusted)
		}
		return fmt.Errorf("pkgtrust: remove %q: %w", path, err)
	}
	return nil
}

// VerifyIdentity checks that identity/issuer is present in the trust
// store.
func VerifyIdentity(identity, issuer string) error {
	// kevin.cue's identity/issuer alone can't be the trust boundary: Fulcio
	// certifies any OIDC identity, so a store outside kevin.cue is what
	// actually gates it - the same principle Keyring.Verify applies to a
	// minisign key id.
	path := filepath.Join(IdentityDir(), identityFile(identity, issuer))
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pkgtrust: %q/%q: %w", identity, issuer, ErrIdentityUntrusted)
		}
		return fmt.Errorf("pkgtrust: stat %q: %w", path, err)
	}
	return nil
}
