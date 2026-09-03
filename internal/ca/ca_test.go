package ca_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/state"
)

func TestManager_LoadOrGenerateRoot(t *testing.T) {
	t.Run("is an authority that can sign one level", func(t *testing.T) {
		root, _ := newRoot(t)
		cert := root.Certificate()

		assert.True(t, cert.IsCA)
		assert.Equal(t, ca.RootCommonName, cert.Subject.CommonName)
		assert.Equal(t, 1, cert.MaxPathLen, "the root must be able to sign an authority for a project")
		assert.NotZero(t, cert.KeyUsage&x509.KeyUsageCertSign)
		assert.Equal(t, cert.Subject, cert.Issuer, "the root is self-signed")
	})

	t.Run("protects its key", func(t *testing.T) {
		_, dir := newRoot(t)

		info, err := os.Stat(filepath.Join(dir, ca.RootKeyFile))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

		info, err = os.Stat(dir)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})

	t.Run("is stable across calls", func(t *testing.T) {
		m := newManager(t, "demo")

		first, err := m.LoadOrGenerateRoot()
		require.NoError(t, err)

		again, err := m.LoadOrGenerateRoot()
		require.NoError(t, err)

		assert.Equal(t, first.PEM(), again.PEM(),
			"a second run must reuse the root, or every trust store goes stale")
	})

	t.Run("honors a custom lifetime", func(t *testing.T) {
		dir := freshDir(t)
		t.Setenv(state.UserStateDirEnv, dir)

		m := ca.NewManager("cwd", "", "demo", ca.Options{RootLifetime: time.Hour})
		root, err := m.LoadOrGenerateRoot()
		require.NoError(t, err)

		assert.WithinDuration(t, time.Now().Add(time.Hour), root.Certificate().NotAfter, time.Minute)
	})

	t.Run("tolerates an absent or half-written directory", func(t *testing.T) {
		tests := []struct {
			name  string
			setup func(t *testing.T, dir string)
		}{
			{name: "an empty directory", setup: func(*testing.T, string) {}},
			{
				name: "a certificate without a key",
				setup: func(t *testing.T, dir string) {
					t.Helper()
					require.NoError(t, os.WriteFile(filepath.Join(dir, ca.RootCertFile), []byte("x"), 0o600))
				},
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv(state.UserStateDirEnv, dir)
				tt.setup(t, dir)

				// A directory without a root is not an error, because the
				// root is created on demand.
				m := ca.NewManager("cwd", "", "demo", ca.Options{})
				root, err := m.LoadOrGenerateRoot()
				require.NoError(t, err)
				assert.NotEmpty(t, root.PEM())
			})
		}
	})

	t.Run("rejects files that are not usable", func(t *testing.T) {
		validCert, validKey := validRootPair(t)

		tests := []struct {
			name    string
			certPEM []byte
			keyPEM  []byte
		}{
			{
				name:    "cert file has no PEM block",
				certPEM: []byte("not pem"),
				keyPEM:  validKey,
			},
			{
				name:    "key file has no PEM block",
				certPEM: validCert,
				keyPEM:  []byte("not pem"),
			},
			{
				name:    "cert PEM does not decode as a certificate",
				certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("garbage")}),
				keyPEM:  validKey,
			},
			{
				name:    "cert is not an authority",
				certPEM: nonCACertPEM(t),
				keyPEM:  validKey,
			},
			{
				name:    "key PEM does not decode as a private key",
				certPEM: validCert,
				keyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("garbage")}),
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv(state.UserStateDirEnv, dir)
				require.NoError(t, os.WriteFile(filepath.Join(dir, ca.RootCertFile), tt.certPEM, 0o600))
				require.NoError(t, os.WriteFile(filepath.Join(dir, ca.RootKeyFile), tt.keyPEM, 0o600))

				m := ca.NewManager("cwd", "", "demo", ca.Options{})
				_, err := m.LoadOrGenerateRoot()
				require.ErrorIs(t, err, ca.ErrInvalid)
			})
		}
	})

	t.Run("reports a read error that is not a missing file", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(state.UserStateDirEnv, dir)
		// A directory where a file is expected fails to read for a reason
		// other than "not found", which readFile must not swallow.
		require.NoError(t, os.Mkdir(filepath.Join(dir, ca.RootCertFile), 0o755))

		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		_, err := m.LoadOrGenerateRoot()
		require.Error(t, err)
		require.NotErrorIs(t, err, ca.ErrNotFound)
	})

	t.Run("reports an error when the state directory cannot be created", func(t *testing.T) {
		// The parent exists but is not writable, so the state directory
		// itself is missing (a plain ENOENT that readFile treats as
		// ErrNotFound) yet MkdirAll cannot create it.
		parent := filepath.Join(t.TempDir(), "parent")
		require.NoError(t, os.Mkdir(parent, 0o500))
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		t.Setenv(state.UserStateDirEnv, filepath.Join(parent, "state"))

		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		_, err := m.LoadOrGenerateRoot()
		require.Error(t, err)
	})

	t.Run("reports an error when a file cannot be written", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv(state.UserStateDirEnv, dir)
		// The directory exists (so MkdirAll is a no-op) but is read-only,
		// so the write of the generated files fails.
		require.NoError(t, os.Chmod(dir, 0o500))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		_, err := m.LoadOrGenerateRoot()
		require.Error(t, err)
	})
}

func TestManager_LoadOrGenerateIntermediate(t *testing.T) {
	t.Run("loads the root automatically if not already loaded", func(t *testing.T) {
		t.Setenv(state.UserStateDirEnv, freshDir(t))
		t.Setenv(state.ProjectStateDirEnv, freshDir(t))

		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		project, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)
		assert.NotEmpty(t, project.RootPEM())
	})

	t.Run("chains to the root", func(t *testing.T) {
		m, root, _ := newManagerWithRoot(t)
		project, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		cert := project.Certificate()
		assert.True(t, cert.IsCA)
		assert.Equal(t, ca.IntermediateCommonName+" - Project demo", cert.Subject.CommonName)
		assert.Equal(t, 0, cert.MaxPathLen, "an authority of a project signs leaves only")
		assert.True(t, cert.MaxPathLenZero)
		assert.Equal(t, root.Certificate().Subject, cert.Issuer)

		require.NoError(t, cert.CheckSignatureFrom(root.Certificate()))
	})

	t.Run("honors a custom lifetime", func(t *testing.T) {
		t.Setenv(state.UserStateDirEnv, freshDir(t))
		t.Setenv(state.ProjectStateDirEnv, freshDir(t))

		m := ca.NewManager("cwd", "", "demo", ca.Options{IntermediateLifetime: time.Hour})
		project, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		assert.WithinDuration(t, time.Now().Add(time.Hour), project.Certificate().NotAfter, time.Minute)
	})

	t.Run("writes the chain and the root", func(t *testing.T) {
		m, root, dir := newManagerWithRoot(t)
		project, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		// The certificate file carries two blocks, so that the proxy serves
		// the intermediate with every leaf.
		assert.Equal(t, 2, countPEM(t, project.PEM()), "the file must hold the chain")

		// A plugin reads the root from the project directory.
		rootOnDisk, err := os.ReadFile(filepath.Join(dir, ca.RootCertFile))
		require.NoError(t, err)
		assert.Equal(t, root.PEM(), string(rootOnDisk))

		assert.Equal(t, root.PEM(), project.RootPEM(),
			"RootPEM is what a client trusts and what a container mounts")

		info, err := os.Stat(filepath.Join(dir, ca.KeyFile))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})

	t.Run("a leaf verifies against the root alone", func(t *testing.T) {
		m, _, _ := newManagerWithRoot(t)
		project, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		// This is the whole point of the topology. A client trusts the root
		// only, and the chain that the proxy serves must close the gap.
		signer, err := project.TLSCertificate()
		require.NoError(t, err)
		require.Len(t, signer.Certificate, 2, "the signer must carry the intermediate and the root")

		intermediates := x509.NewCertPool()
		for _, der := range signer.Certificate {
			cert, parseErr := x509.ParseCertificate(der)
			require.NoError(t, parseErr)
			intermediates.AddCert(cert)
		}

		_, err = project.Certificate().Verify(x509.VerifyOptions{
			Roots:         project.Pool(),
			Intermediates: intermediates,
		})
		require.NoError(t, err, "the authority of the project must verify against the root")
	})

	t.Run("two projects share one root", func(t *testing.T) {
		rootDir := freshDir(t)

		t.Setenv(state.UserStateDirEnv, rootDir)
		t.Setenv(state.ProjectStateDirEnv, freshDir(t))
		m1 := ca.NewManager("cwd", "", "one", ca.Options{})
		one, err := m1.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		t.Setenv(state.UserStateDirEnv, rootDir)
		t.Setenv(state.ProjectStateDirEnv, freshDir(t))
		m2 := ca.NewManager("cwd", "", "two", ca.Options{})
		two, err := m2.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		assert.Equal(t, one.RootPEM(), two.RootPEM(), "one anchor serves every project")
		assert.NotEqual(t, one.PEM(), two.PEM(), "each project signs with its own key")
	})

	t.Run("is replaced when the root changes", func(t *testing.T) {
		projectDir := freshDir(t)

		t.Setenv(state.UserStateDirEnv, freshDir(t))
		t.Setenv(state.ProjectStateDirEnv, projectDir)
		m1 := ca.NewManager("cwd", "", "demo", ca.Options{})
		before, err := m1.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		// A user who deletes the home directory gets a new root. An
		// authority signed by the old root verifies against nothing.
		t.Setenv(state.UserStateDirEnv, freshDir(t))
		t.Setenv(state.ProjectStateDirEnv, projectDir)
		m2 := ca.NewManager("cwd", "", "demo", ca.Options{})
		second, err := m2.LoadOrGenerateRoot()
		require.NoError(t, err)
		after, err := m2.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		assert.NotEqual(t, before.Certificate().SerialNumber, after.Certificate().SerialNumber,
			"a stale authority must be replaced")
		require.NoError(t, after.Certificate().CheckSignatureFrom(second.Certificate()))
	})

	t.Run("is stable while the root is stable", func(t *testing.T) {
		m, _, _ := newManagerWithRoot(t)

		first, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)
		second, err := m.LoadOrGenerateIntermediate()
		require.NoError(t, err)

		assert.Equal(t, first.Certificate().SerialNumber, second.Certificate().SerialNumber,
			"a second run must reuse the authority of the project")
	})

	t.Run("reports an error when the project directory cannot be created", func(t *testing.T) {
		t.Setenv(state.UserStateDirEnv, freshDir(t))

		parent := filepath.Join(t.TempDir(), "parent")
		require.NoError(t, os.Mkdir(parent, 0o500))
		t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
		t.Setenv(state.ProjectStateDirEnv, filepath.Join(parent, "project"))

		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		_, err := m.LoadOrGenerateIntermediate()
		require.Error(t, err)
	})
}

func TestCA_TLSCertificate(t *testing.T) {
	t.Run("errors when the certificate and key do not match", func(t *testing.T) {
		_, dirA := newRoot(t)
		_, dirB := newRoot(t)

		certA, err := os.ReadFile(filepath.Join(dirA, ca.RootCertFile))
		require.NoError(t, err)
		keyB, err := os.ReadFile(filepath.Join(dirB, ca.RootKeyFile))
		require.NoError(t, err)

		frankenDir := filepath.Join(t.TempDir(), "franken")
		require.NoError(t, os.MkdirAll(frankenDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(frankenDir, ca.RootCertFile), certA, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(frankenDir, ca.RootKeyFile), keyB, 0o600))

		t.Setenv(state.UserStateDirEnv, frankenDir)
		m := ca.NewManager("cwd", "", "demo", ca.Options{})
		mismatched, err := m.LoadOrGenerateRoot()
		require.NoError(t, err, "loading does not check that the certificate and key match")

		_, err = mismatched.TLSCertificate()
		require.Error(t, err)
	})
}

func TestProjectCertPath(t *testing.T) {
	got := ca.ProjectCertPath("/work/project", "demo")
	assert.Equal(t, filepath.Join(state.ProjectStateDir("/work/project", "demo"), ca.CertFile), got)
}

func TestProjectKeyPath(t *testing.T) {
	got := ca.ProjectKeyPath("/work/project", "demo")
	assert.Equal(t, filepath.Join(state.ProjectStateDir("/work/project", "demo"), ca.KeyFile), got)
}

func TestProjectVars(t *testing.T) {
	got := ca.ProjectVars("/work/project", "demo")
	assert.Equal(t, map[string]string{
		"root_cert": ca.RootCertPath(),
		"ca_cert":   ca.ProjectCertPath("/work/project", "demo"),
		"ca_key":    ca.ProjectKeyPath("/work/project", "demo"),
	}, got)
}

// freshDir returns a path to a directory that does not exist yet, inside a
// fresh temporary directory. LoadOrGenerateRoot and LoadOrGenerateIntermediate
// only set 0700 permissions when they create a directory themselves, so
// tests that check permissions must not hand them one that already exists.
func freshDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state")
}

// newManager builds a Manager pointed at a fresh temporary user-state and
// project-state directory.
func newManager(t *testing.T, project string) *ca.Manager {
	t.Helper()
	t.Setenv(state.UserStateDirEnv, freshDir(t))
	t.Setenv(state.ProjectStateDirEnv, freshDir(t))
	return ca.NewManager("cwd", "", project, ca.Options{})
}

// newRoot builds a root in a temporary directory.
func newRoot(t *testing.T) (*ca.CA, string) {
	t.Helper()
	dir := freshDir(t)
	t.Setenv(state.UserStateDirEnv, dir)
	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	root, err := m.LoadOrGenerateRoot()
	require.NoError(t, err)
	return root, dir
}

// newManagerWithRoot builds a Manager whose root is already loaded, and
// reports the project-state directory it was constructed with.
func newManagerWithRoot(t *testing.T) (*ca.Manager, *ca.CA, string) {
	t.Helper()
	projectDir := freshDir(t)
	t.Setenv(state.UserStateDirEnv, freshDir(t))
	t.Setenv(state.ProjectStateDirEnv, projectDir)
	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	root, err := m.LoadOrGenerateRoot()
	require.NoError(t, err)
	return m, root, projectDir
}

// countPEM reports how many PEM blocks a string holds.
func countPEM(t *testing.T, s string) int {
	t.Helper()

	n := 0
	for rest := []byte(s); ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return n
		}
		n++
	}
}

// validRootPair returns a well-formed root certificate and key, as PEM, to
// use as the "everything but this one field" baseline in invalid-file cases.
func validRootPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	_, rootDir := newRoot(t)
	certPEM, err := os.ReadFile(filepath.Join(rootDir, ca.RootCertFile))
	require.NoError(t, err)
	keyPEM, err := os.ReadFile(filepath.Join(rootDir, ca.RootKeyFile))
	require.NoError(t, err)
	return certPEM, keyPEM
}

// nonCACertPEM builds a self-signed leaf certificate, valid PEM but not an
// authority, to exercise the "cert is not an authority" rejection path.
func nonCACertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leaf"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
