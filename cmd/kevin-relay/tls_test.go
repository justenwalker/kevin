package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// controlCertEnv sets the relay's control TLS environment to one
// self-signed certificate that serves as the server pair and the client CA.
func controlCertEnv(t *testing.T) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: controlServerCN},
		DNSNames:              []string{controlServerCN},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	t.Setenv(tlsCertEnv, string(certPEM))
	t.Setenv(tlsKeyEnv, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	t.Setenv(tlsClientCAEnv, string(certPEM))
}

func TestControlTLSConfig(t *testing.T) {
	t.Run("requires a client certificate that chains to the project root", func(t *testing.T) {
		controlCertEnv(t)

		cfg, err := controlTLSConfig()

		require.NoError(t, err)
		assert.Len(t, cfg.Certificates, 1)
		assert.NotNil(t, cfg.ClientCAs)
	})

	t.Run("rejects a missing server certificate", func(t *testing.T) {
		controlCertEnv(t)
		t.Setenv(tlsCertEnv, "")

		_, err := controlTLSConfig()

		require.ErrorContains(t, err, "parse control tls certificate")
	})

	t.Run("rejects a client CA that holds no certificate", func(t *testing.T) {
		controlCertEnv(t)
		t.Setenv(tlsClientCAEnv, "not pem")

		_, err := controlTLSConfig()

		require.ErrorIs(t, err, ErrInvalidControlClientCA)
	})
}
