package proxy_test

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/proxy"
)

// selfSignedTLSUpstream starts an HTTPS server presenting a self-signed leaf
// for host - its own certificate, not one kevin's CA (or any other shared
// authority) ever touches, mirroring a workload that mints its own TLS
// entirely on its own, such as a cert-manager-issued certificate.
func selfSignedTLSUpstream(t *testing.T, host, body string) *httptest.Server {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{host},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// clientTrusting builds an http.Client that reaches p through the proxy and
// trusts only pool - not the kevin CA startTestProxyServingWithAuthority's
// own client trusts - so a successful request proves the client validated
// whatever certificate the connection actually presented.
func clientTrusting(t *testing.T, p *proxy.Proxy, pool *x509.CertPool) *http.Client {
	t.Helper()
	assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

	proxyURL, err := url.Parse("http://" + p.Addr())
	require.NoError(t, err)
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}
}

// TestTunnelRoute covers a route that asks to skip kevin's MITM: the client
// must see the upstream's own certificate, not a kevin-signed leaf, and the
// ordinary MITM path must stay untouched for every other route.
func TestTunnelRoute(t *testing.T) {
	t.Run("skip_mitm tunnels straight through to the upstream's own certificate", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, kevinClient := startTestProxyServingWithAuthority(t, authority, nil, true)

		const host = "certmanager.kevin.test"
		target := selfSignedTLSUpstream(t, host, "from the workload's own cert")

		p.AddRoutes(proxy.Route{Host: host, Upstream: getTestURLHost(t, target.URL), TLS: true, SkipMITM: true})

		ownPool := x509.NewCertPool()
		ownPool.AddCert(target.Certificate())
		ownClient := clientTrusting(t, p, ownPool)

		resp, body := getTestURL(t, ownClient, "https://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "from the workload's own cert", body)
		require.NotEmpty(t, resp.TLS.PeerCertificates)
		assert.True(t, resp.TLS.PeerCertificates[0].Equal(target.Certificate()),
			"the client must see the workload's own certificate, not a kevin-signed leaf")

		_, err := kevinClient.Get("https://" + host + "/") //nolint:noctx // test-only request, no context needed
		assert.Error(t, err, "a client that trusts only the kevin CA must not be able to verify the workload's own certificate")
	})

	t.Run("skip_mitm false keeps the ordinary MITM path for a tls: true route", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, client := startTestProxyServingWithAuthority(t, authority, nil, true)

		const host = "still-mitmed.kevin.test"
		target := createTestTLSUpstream(t, authority, host, "still mitm'd")
		p.AddRoutes(proxy.Route{Host: host, Upstream: getTestURLHost(t, target.URL), TLS: true})

		resp, body := getTestURL(t, client, "https://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "still mitm'd", body)
		require.NotEmpty(t, resp.TLS.PeerCertificates)
		assert.Equal(t, host, resp.TLS.PeerCertificates[0].Subject.CommonName,
			"a tls: true route must still be MITM'd with a kevin-signed leaf when skip_mitm is false")
	})

	t.Run("skip_mitm is ignored for a route whose upstream is not itself TLS", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)

		const host = "plain-http.kevin.test"
		target := createTestUpstream(t, "plain http, mitm required")
		p.AddRoutes(proxy.Route{Host: host, Upstream: getTestURLHost(t, target.URL), SkipMITM: true})

		resp, body := getTestURL(t, client, "https://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "plain http, mitm required", body)
		require.NotNil(t, resp.TLS)
		assert.Equal(t, host, resp.TLS.PeerCertificates[0].Subject.CommonName,
			"a plain-HTTP upstream has no certificate to tunnel, so it must still be MITM'd regardless of skip_mitm")
	})

	t.Run("relayed route tunnels through the SOCKS5 relay", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, _ := startTestProxyServingWithAuthority(t, authority, nil, true)

		const host = "relayed-skip.kevin.test"
		target := selfSignedTLSUpstream(t, host, "relayed and skipped")
		relay := startSOCKS5(t)

		p.AddRoutes(proxy.Route{
			Host:     host,
			Upstream: "socks5://" + relay + "/" + getTestURLHost(t, target.URL),
			TLS:      true,
			SkipMITM: true,
		})

		ownPool := x509.NewCertPool()
		ownPool.AddCert(target.Certificate())
		ownClient := clientTrusting(t, p, ownPool)

		resp, body := getTestURL(t, ownClient, "https://"+host+"/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "relayed and skipped", body)
	})

	t.Run("a dial failure to the upstream gets a 502, not a hang", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		const host = "unreachable-skip.kevin.test"
		p.AddRoutes(proxy.Route{Host: host, Upstream: "127.0.0.1:1", TLS: true, SkipMITM: true})

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		_, err = io.WriteString(conn, "CONNECT "+host+":443 HTTP/1.1\r\nHost: "+host+":443\r\n\r\n")
		require.NoError(t, err)

		line, err := bufio.NewReader(conn).ReadString('\n')
		require.NoError(t, err)
		assert.Contains(t, line, "502")
	})

	t.Run("records one entry for the whole tunnel, not just the dial", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, err := proxy.New(authority, "kevin.home", nil, true)
		require.NoError(t, err)

		var mu sync.Mutex
		var records []proxy.Record
		p.OnRecord(func(rec proxy.Record) {
			mu.Lock()
			defer mu.Unlock()
			records = append(records, rec)
		})

		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- p.Serve(ctx, ln) }()
		t.Cleanup(func() {
			cancel()
			require.NoError(t, <-done)
		})

		const host = "recorded-skip.kevin.test"
		target := selfSignedTLSUpstream(t, host, "recorded")
		p.AddRoutes(proxy.Route{Host: host, Upstream: getTestURLHost(t, target.URL), TLS: true, SkipMITM: true})

		ownPool := x509.NewCertPool()
		ownPool.AddCert(target.Certificate())
		proxyURL, err := url.Parse("http://" + ln.Addr().String())
		require.NoError(t, err)
		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				TLSClientConfig:   &tls.Config{RootCAs: ownPool, MinVersion: tls.VersionTLS12},
				DisableKeepAlives: true,
			},
		}

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+host+"/", nil)
		require.NoError(t, err)
		resp, err := client.Do(req)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		assert.Equal(t, "recorded", string(body))

		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(records) == 1
		}, time.Second, time.Millisecond, "the tunnel must be recorded once it closes")

		mu.Lock()
		rec := records[0]
		mu.Unlock()
		assert.Equal(t, http.MethodConnect, rec.Method)
		assert.Equal(t, host, rec.Host)
		assert.Equal(t, http.StatusOK, rec.Status)
		assert.True(t, rec.Routed)
		assert.False(t, rec.Denied)
		assert.False(t, rec.Time.IsZero())
	})
}
