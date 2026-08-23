// Package pkgtrust loads a local keyring of minisign public keys from a
// directory and verifies kevin plugin package signatures against them.
package pkgtrust

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jedisct1/go-minisign"
)

// Dir is ~/.kevin/trusted-keys - global, shared across every project.
func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kevin", "trusted-keys")
	}
	return filepath.Join(home, ".kevin", "trusted-keys")
}

// Keyring is the trusted public keys, indexed by minisign key id.
type Keyring map[[8]byte]minisign.PublicKey

// Load reads every file in Dir as a minisign public key. A Dir that does not
// exist yet is an empty Keyring, not an error.
func Load() (Keyring, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return Keyring{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pkgtrust: read %q: %w", Dir(), err)
	}
	kr := make(Keyring, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(Dir(), e.Name())
		pub, err := minisign.NewPublicKeyFromFile(path)
		if err != nil {
			return nil, fmt.Errorf("pkgtrust: %q: %w: %w", path, ErrBadKey, err)
		}
		kr[pub.KeyId] = pub
	}
	return kr, nil
}

// Verify reports whether sig, over message, comes from a key in kr.
func (kr Keyring) Verify(sig minisign.Signature, message []byte) error {
	pub, ok := kr[sig.KeyId]
	if !ok {
		return fmt.Errorf("pkgtrust: key id %x: %w", sig.KeyId, ErrUnknownKeyID)
	}
	if _, err := pub.Verify(message, sig); err != nil {
		return fmt.Errorf("pkgtrust: %w: %w", ErrSignatureInvalid, err)
	}
	return nil
}

// Add validates pubkeyPath as a minisign public key and copies it into Dir,
// named by its hex key id. Adding a key already in the store is a no-op. Add
// returns the key's hex id.
func Add(pubkeyPath string) (string, error) {
	pub, err := minisign.NewPublicKeyFromFile(pubkeyPath)
	if err != nil {
		return "", fmt.Errorf("pkgtrust: %q: %w: %w", pubkeyPath, ErrBadKey, err)
	}
	data, err := os.ReadFile(pubkeyPath) //nolint:gosec // pubkeyPath names a caller-chosen key file, the whole point
	if err != nil {
		return "", fmt.Errorf("pkgtrust: read %q: %w", pubkeyPath, err)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", fmt.Errorf("pkgtrust: create %q: %w", Dir(), err)
	}
	id := hex.EncodeToString(pub.KeyId[:])                                             // hex only: safe to join into a path
	if err := os.WriteFile(filepath.Join(Dir(), id+".pub"), data, 0o600); err != nil { //nolint:gosec // id is hex-encoded above, never a path-escaping string
		return "", fmt.Errorf("pkgtrust: write %q: %w", id, err)
	}
	return id, nil
}

// KeyInfo is one trusted key, for List.
type KeyInfo struct {
	// ID is the key's hex-encoded minisign key id.
	ID string
	// File is the key's file name within Dir.
	File string
}

// List reports every key in the trust store. A Dir that does not exist yet
// reports no keys, not an error. A file that doesn't parse as a minisign
// public key is skipped, not an error - the trust store may hold stray
// files a user dropped in by hand.
func List() ([]KeyInfo, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pkgtrust: read %q: %w", Dir(), err)
	}
	out := make([]KeyInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		pub, err := minisign.NewPublicKeyFromFile(filepath.Join(Dir(), e.Name()))
		if err != nil {
			continue
		}
		out = append(out, KeyInfo{ID: hex.EncodeToString(pub.KeyId[:]), File: e.Name()})
	}
	return out, nil
}

// Remove deletes the key with the given hex id from the trust store.
func Remove(keyID string) error {
	path := filepath.Join(Dir(), keyID+".pub")
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("pkgtrust: %q: %w", keyID, ErrUnknownKeyID)
		}
		return fmt.Errorf("pkgtrust: remove %q: %w", keyID, err)
	}
	return nil
}
