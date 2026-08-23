package proxy

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCertSigner(t *testing.T) {
	t.Run("rejects a signer with no certificate", func(t *testing.T) {
		_, err := newCertSigner(tls.Certificate{})
		assert.ErrorIs(t, err, ErrNoSigningCertificate)
	})

	t.Run("rejects a non-ECDSA signing key", func(t *testing.T) {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)

		_, err = newCertSigner(newTestTLSCertificate(t, "kevin test root", key))
		assert.ErrorIs(t, err, ErrUnsupportedSigningKey)
	})

	t.Run("builds from an ECDSA signer", func(t *testing.T) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)

		cs, err := newCertSigner(newTestTLSCertificate(t, "kevin test root", key))
		require.NoError(t, err)
		assert.Equal(t, "kevin test root", cs.issuer.Subject.CommonName)
	})
}

func TestCertSignerLeafFor(t *testing.T) {
	t.Run("mints a leaf chained to the issuer", func(t *testing.T) {
		cs := newTestCertSigner(t)

		cert, err := cs.leafFor("example.kevin.test")
		require.NoError(t, err)
		require.Len(t, cert.Certificate, 2, "leaf plus the issuer appended after it")

		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		require.NoError(t, err)
		assert.Equal(t, "example.kevin.test", leaf.Subject.CommonName)
		assert.Equal(t, "kevin test root", leaf.Issuer.CommonName)
		assert.Equal(t, cs.chain[0], cert.Certificate[1], "the issuer's own certificate must follow the leaf")
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

// newTestCertSigner builds a certSigner for testing with a self-signed CA.
func newTestCertSigner(t *testing.T) *certSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	cs, err := newCertSigner(newTestTLSCertificate(t, "kevin test root", key))
	require.NoError(t, err)
	return cs
}

// newTestTLSCertificate creates a tls.Certificate with a self-signed CA certificate.
func newTestTLSCertificate(t *testing.T, cn string, key crypto.Signer) tls.Certificate {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
