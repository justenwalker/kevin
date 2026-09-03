package proxy_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/proxy"
)

// mintTestLeaf signs a leaf certificate for host with authority's own
// signing key - the same chain [ca.CA.TLSCertificate] carries, mirroring
// certs.go's own certSigner.sign. A test TLS upstream server presenting
// this chain is exactly what a builtin:container step's own TLS-terminating
// workload would present in production: a leaf the project's own CA signs.
func mintTestLeaf(t *testing.T, authority *ca.CA, host string) tls.Certificate {
	t.Helper()

	signer, err := authority.TLSCertificate()
	require.NoError(t, err)
	issuer, err := x509.ParseCertificate(signer.Certificate[0])
	require.NoError(t, err)
	issuerKey, ok := signer.PrivateKey.(*ecdsa.PrivateKey)
	require.True(t, ok)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{host},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, issuer, &key.PublicKey, issuerKey)
	require.NoError(t, err)

	chain := append([][]byte{der}, signer.Certificate...)
	return tls.Certificate{Certificate: chain, PrivateKey: key}
}

// createTestTLSUpstream starts an HTTPS server presenting a leaf that
// authority signs for host, reporting the Host header it received - the
// same shape createTestUpstream reports, over TLS instead of plain HTTP.
func createTestTLSUpstream(t *testing.T, authority *ca.CA, host, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Host", r.Host)
		_, _ = w.Write([]byte(body))
	}))
	leaf := mintTestLeaf(t, authority, host)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestDialTLSContext covers a tls: true route's outbound TLS handshake,
// both plain and relayed - the certificate must verify against the kevin
// CA root with no "kevin ca install" needed, and against the hostname the
// client actually asked for, not the relay's own dial address.
func TestDialTLSContext(t *testing.T) {
	t.Run("trusts the kevin CA root with no system install", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, client := startTestProxyServingWithAuthority(t, authority, nil, true)

		const host = "tls-upstream.kevin.test"
		target := createTestTLSUpstream(t, authority, host, "from the tls workload")

		p.AddRoutes(proxy.Route{Host: host, Upstream: getTestURLHost(t, target.URL), TLS: true})

		resp, body := getTestURL(t, client, "http://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "from the tls workload", body)
	})

	t.Run("verifies a relayed route against the real upstream hostname", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, client := startTestProxyServingWithAuthority(t, authority, nil, true)

		const host = "relayed-tls.kevin.test"
		target := createTestTLSUpstream(t, authority, host, "from the relayed tls workload")
		relay := startSOCKS5(t)

		p.AddRoutes(proxy.Route{
			Host:     host,
			Upstream: "socks5://" + relay + "/" + getTestURLHost(t, target.URL),
			TLS:      true,
		})

		resp, body := getTestURL(t, client, "http://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "from the relayed tls workload", body)
	})
}
