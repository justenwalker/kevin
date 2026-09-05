package proxy

import (
	"crypto/x509"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/state"
)

func TestCertSignerLeafFor(t *testing.T) {
	t.Run("mints a leaf chained to the issuer", func(t *testing.T) {
		cs := newTestCertSigner(t)

		cert, err := cs.leafFor("example.kevin.test")
		require.NoError(t, err)
		require.Len(t, cert.Certificate, 3, "leaf, then the intermediate, then the root")

		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err)
		assert.Equal(t, "example.kevin.test", leaf.Subject.CommonName)
		assert.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	})

	t.Run("sets a DNS SAN for a hostname", func(t *testing.T) {
		cs := newTestCertSigner(t)

		cert, err := cs.leafFor("dns.kevin.test")
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err)
		assert.Equal(t, []string{"dns.kevin.test"}, leaf.DNSNames)
		assert.Empty(t, leaf.IPAddresses)
	})

	t.Run("sets an IP SAN for an IP host", func(t *testing.T) {
		cs := newTestCertSigner(t)

		cert, err := cs.leafFor("127.0.0.1")
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err)
		require.Len(t, leaf.IPAddresses, 1)
		assert.True(t, leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")))
		assert.Empty(t, leaf.DNSNames)
	})

	t.Run("caches repeated calls for the same host", func(t *testing.T) {
		cs := newTestCertSigner(t)

		first, err := cs.leafFor("cached.kevin.test")
		require.NoError(t, err)
		second, err := cs.leafFor("cached.kevin.test")
		require.NoError(t, err)
		assert.Same(t, first, second, "a cached leaf must not be re-signed")
	})
}

// newTestCertSigner builds a certSigner from a freshly generated project
// authority.
func newTestCertSigner(t *testing.T) *certSigner {
	t.Helper()
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "demo", ca.Options{})
	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)

	return newCertSigner(authority)
}
