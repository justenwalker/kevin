package engine

import (
	"archive/tar"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jedisct1/go-minisign"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pkgtrust"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
)

// writePackage builds a fixture plugin package tar at dir/pkg.tar, with a
// manifest built from edit, and returns its path.
func writePackage(t *testing.T, dir string, edit func(*pluginpkg.Manifest)) string {
	t.Helper()
	m := pluginpkg.Manifest{
		ManifestVersion: pluginpkg.CurrentManifestVersion,
		Name:            "acme",
		Version:         "1.0.0",
		Entrypoint:      "acme-plugin",
		Args:            []string{"--from-manifest"},
	}
	if edit != nil {
		edit(&m)
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)

	path := filepath.Join(dir, "pkg.tar")
	f, err := os.Create(path)
	require.NoError(t, err)

	tw := tar.NewWriter(f)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: pluginpkg.ManifestFile, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data)),
	}))
	_, err = tw.Write(data)
	require.NoError(t, err)

	body := []byte("#!/bin/sh\n")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "acme-plugin", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
	}))
	_, err = tw.Write(body)
	require.NoError(t, err)

	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
	return path
}

// signingKey is a freshly generated, unencrypted minisign key pair, for
// exercising the signed: true path in resolveSpec.
type signingKey struct {
	sk minisign.PrivateKey
}

func newSigningKey(t *testing.T) signingKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var keyID [8]byte
	_, err = rand.Read(keyID[:])
	require.NoError(t, err)

	sk := minisign.PrivateKey{SignatureAlgorithm: [2]byte{'E', 'd'}, KeyId: keyID}
	copy(sk.SecretKey[:], priv)
	return signingKey{sk: sk}
}

// trust adds k's public key to the trust store (HOME must already point at
// a temp dir).
func (k signingKey) trust(t *testing.T) {
	t.Helper()
	pub := k.sk.PublicKey()
	raw := make([]byte, 0, 42)
	raw = append(raw, pub.SignatureAlgorithm[:]...)
	raw = append(raw, pub.KeyId[:]...)
	raw = append(raw, pub.PublicKey[:]...)
	text := "untrusted comment: test key\n" + base64.StdEncoding.EncodeToString(raw) + "\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "signer.pub")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	_, err := pkgtrust.Add(path)
	require.NoError(t, err)
}

// signFile signs path's content and writes the detached signature to
// path+".minisig".
func (k signingKey) signFile(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := k.sk.Sign(data, minisign.SignOptions{Hashed: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".minisig", sig.Encode(), 0o600))
}

// stubExecutablePath replaces executablePath for the duration of the test,
// and restores it on cleanup.
func stubExecutablePath(t *testing.T, path string, err error) {
	t.Helper()
	original := executablePath
	t.Cleanup(func() { executablePath = original })
	executablePath = func() (string, error) { return path, err }
}

func TestResolveSpec(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(t *testing.T) (plugin, dir string, specs map[string]config.PluginSpec)
		wantErr         error
		wantAnyErr      bool
		wantErrContains string
		check           func(t *testing.T, dir string, spec pluginhost.Spec)
	}{
		{
			name: "builds a builtin command",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				stubExecutablePath(t, "/path/to/kevin", nil)
				return "builtin", "/project", nil
			},
			check: func(t *testing.T, _ string, spec pluginhost.Spec) {
				t.Helper()
				assert.Equal(t, "/path/to/kevin", spec.Cmd)
				assert.Equal(t, []string{"plugin", "run", "builtin"}, spec.Args)
				assert.Equal(t, "/project", spec.Dir)
			},
		},
		{
			name: "reports a failure to locate the binary",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				stubExecutablePath(t, "", os.ErrNotExist)
				return "builtin", "/project", nil
			},
			wantAnyErr: true,
		},
		{
			name: "uses the declared plugin",
			setup: func(*testing.T) (string, string, map[string]config.PluginSpec) {
				return "acme", "/project", map[string]config.PluginSpec{
					"acme": {Cmd: "./widget", Args: []string{"--verbose"}, Env: map[string]string{"K": "V"}},
				}
			},
			check: func(t *testing.T, _ string, spec pluginhost.Spec) {
				t.Helper()
				assert.Equal(t, "./widget", spec.Cmd)
				assert.Equal(t, []string{"--verbose"}, spec.Args)
				assert.Equal(t, map[string]string{"K": "V"}, spec.Env)
				assert.Equal(t, "/project", spec.Dir)
			},
		},
		{
			name: "reports an unknown plugin",
			setup: func(*testing.T) (string, string, map[string]config.PluginSpec) {
				return "nope", "/project", map[string]config.PluginSpec{"acme": {Cmd: "./widget"}}
			},
			wantErr:         config.ErrUnknownPlugin,
			wantErrContains: "acme",
		},
		{
			name: "extracts a file-source plugin",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				dir := t.TempDir()
				pkgPath := writePackage(t, dir, nil)
				return "acme", dir, map[string]config.PluginSpec{
					"acme": {File: pkgPath, Env: map[string]string{"K": "V"}},
				}
			},
			check: func(t *testing.T, dir string, spec pluginhost.Spec) {
				t.Helper()
				wantDir := filepath.Join(dir, WorkspaceDir, PluginPkgDir, "acme")
				assert.Equal(t, filepath.Join(wantDir, "acme-plugin"), spec.Cmd)
				assert.Equal(t, wantDir, spec.Dir)
				assert.Equal(t, map[string]string{"K": "V"}, spec.Env, "env must come from kevin.cue, never the manifest")
				assert.Equal(t, []string{"--from-manifest"}, spec.Args, "unconfigured args fall through from the manifest")
			},
		},
		{
			name: "overrides manifest args with configured args",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				dir := t.TempDir()
				pkgPath := writePackage(t, dir, nil)
				return "acme", dir, map[string]config.PluginSpec{
					"acme": {File: pkgPath, Args: []string{"--from-kevin-cue"}},
				}
			},
			check: func(t *testing.T, _ string, spec pluginhost.Spec) {
				t.Helper()
				assert.Equal(t, []string{"--from-kevin-cue"}, spec.Args)
			},
		},
		{
			name: "reports a file-source name mismatch",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				dir := t.TempDir()
				pkgPath := writePackage(t, dir, func(m *pluginpkg.Manifest) { m.Name = "other" })
				return "acme", dir, map[string]config.PluginSpec{"acme": {File: pkgPath}}
			},
			wantErr: pluginhost.ErrNameMismatch,
		},
		{
			name: "reports a missing file-source package",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				return "acme", t.TempDir(), map[string]config.PluginSpec{"acme": {File: "./does-not-exist.tar"}}
			},
			wantAnyErr: true,
		},
		{
			name: "reports a bad OCI reference",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				return "acme", t.TempDir(), map[string]config.PluginSpec{"acme": {OCI: "not a valid ref!!"}}
			},
			wantErr: ocipkg.ErrBadReference,
		},
		{
			name: "reports a bad HTTP URL",
			setup: func(t *testing.T) (string, string, map[string]config.PluginSpec) {
				t.Helper()
				return "acme", t.TempDir(), map[string]config.PluginSpec{"acme": {HTTP: "not a valid url!!"}}
			},
			wantErr: httppkg.ErrBadURL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin, dir, specs := tt.setup(t)
			spec, err := resolveSpec(t.Context(), plugin, dir, specs)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)
			case tt.wantAnyErr:
				require.Error(t, err)
			default:
				require.NoError(t, err)
			}
			if tt.wantErrContains != "" {
				assert.Contains(t, err.Error(), tt.wantErrContains)
			}
			if tt.check != nil {
				tt.check(t, dir, spec)
			}
		})
	}

	t.Run("signed via a local file", testResolveSpecSignedFile)
	t.Run("signed via HTTP", testResolveSpecSignedHTTP)
}

func testResolveSpecSignedFile(t *testing.T) {
	t.Run("launches when the package is signed by a trusted key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)

		k := newSigningKey(t)
		k.trust(t)
		k.signFile(t, pkgPath)

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath, Signed: true}}
		spec, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(dir, WorkspaceDir, PluginPkgDir, "acme", "acme-plugin"), spec.Cmd)
	})

	t.Run("fails closed with no signature file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath, Signed: true}}
		_, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.ErrorIs(t, err, pkgtrust.ErrSignatureMissing)
	})

	t.Run("fails closed with a malformed signature file", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)
		require.NoError(t, os.WriteFile(pkgPath+".minisig", []byte("not a minisig file"), 0o600))

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath, Signed: true}}
		_, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.ErrorIs(t, err, pkgtrust.ErrSignatureInvalid)
	})

	t.Run("fails closed when the signer is not in the trust store", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)

		untrusted := newSigningKey(t)
		untrusted.signFile(t, pkgPath)

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath, Signed: true}}
		_, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.ErrorIs(t, err, pkgtrust.ErrUnknownKeyID)
	})

	t.Run("fails closed when the package bytes do not match the signature", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)

		k := newSigningKey(t)
		k.trust(t)
		k.signFile(t, pkgPath)

		// Rewrite the package after signing - same manifest/entrypoint, so
		// it still extracts fine, but the bytes no longer match the
		// signature.
		require.NoError(t, os.WriteFile(pkgPath, []byte("tampered"), 0o600))

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath, Signed: true}}
		_, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.ErrorIs(t, err, pkgtrust.ErrSignatureInvalid)
	})

	t.Run("skips verification when signed is false", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		dir := t.TempDir()
		pkgPath := writePackage(t, dir, nil)

		specs := map[string]config.PluginSpec{"acme": {File: pkgPath}}
		_, err := resolveSpec(t.Context(), "acme", dir, specs)
		require.NoError(t, err)
	})
}

func testResolveSpecSignedHTTP(t *testing.T) {
	t.Run("launches when the package is signed by a trusted key", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		srcDir := t.TempDir()
		pkgPath := writePackage(t, srcDir, nil)
		pkgData, err := os.ReadFile(pkgPath)
		require.NoError(t, err)

		k := newSigningKey(t)
		k.trust(t)
		k.signFile(t, pkgPath)
		sigData, err := os.ReadFile(pkgPath + ".minisig")
		require.NoError(t, err)

		mux := http.NewServeMux()
		mux.HandleFunc("/pkg.tar", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pkgData) })
		mux.HandleFunc("/pkg.tar.minisig", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(sigData) })
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		specs := map[string]config.PluginSpec{"acme": {HTTP: srv.URL + "/pkg.tar", Signed: true}}
		spec, err := resolveSpec(t.Context(), "acme", t.TempDir(), specs)
		require.NoError(t, err)
		assert.Equal(t, "acme-plugin", filepath.Base(spec.Cmd))
	})

	t.Run("fails closed with no signature published", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		srcDir := t.TempDir()
		pkgPath := writePackage(t, srcDir, nil)
		pkgData, err := os.ReadFile(pkgPath)
		require.NoError(t, err)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/pkg.tar" {
				_, _ = w.Write(pkgData)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)

		specs := map[string]config.PluginSpec{"acme": {HTTP: srv.URL + "/pkg.tar", Signed: true}}
		_, err = resolveSpec(t.Context(), "acme", t.TempDir(), specs)
		require.ErrorIs(t, err, httppkg.ErrFetch)
	})
}
