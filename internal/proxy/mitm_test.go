package proxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/proxy"
)

// TestConnect covers handleConnect end to end: terminating TLS with a leaf
// minted from the kevin CA, negotiating whatever protocol the client offers
// over ALPN, and its error paths - a request the proxy cannot hijack, and a
// client that never completes the TLS handshake the CONNECT promised. The
// failure modes must end the attempt without hanging or leaking the
// connection, rather than falling through to MITM traffic that was never
// authenticated.
func TestConnect(t *testing.T) {
	t.Run("terminates TLS with a certificate from the kevin CA", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "secure workload")

		p.AddRoutes(proxy.Route{Host: "secure.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		// The client trusts the kevin CA and nothing else. A plain https
		// request to a name that resolves nowhere therefore proves three
		// things at once: CONNECT works, the proxy minted a leaf for the
		// name, and the route reached the workload.
		resp, body := getTestURL(t, client, "https://secure.kevin.test/")

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "secure workload", body)
		require.NotNil(t, resp.TLS, "the connection must be TLS")
		assert.Equal(t, "secure.kevin.test", resp.TLS.PeerCertificates[0].Subject.CommonName)
	})

	t.Run("negotiates HTTP/2 when the client supports it", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, err := proxy.New(authority, "kevin.home", nil, true)
		require.NoError(t, err)

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

		proxyURL, err := url.Parse("http://" + ln.Addr().String())
		require.NoError(t, err)

		// ForceAttemptHTTP2 makes the client's intent to negotiate h2
		// explicit, rather than relying on whichever way net/http's own
		// defaults happen to fall given the custom TLSClientConfig below.
		client := &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				Proxy:             http.ProxyURL(proxyURL),
				TLSClientConfig:   &tls.Config{RootCAs: authority.Pool(), MinVersion: tls.VersionTLS12},
				ForceAttemptHTTP2: true,
			},
		}

		target := createTestUpstream(t, "via h2")
		p.AddRoutes(proxy.Route{Host: "h2.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		resp, body := getTestURL(t, client, "https://h2.kevin.test/")

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "via h2", body)
		assert.Equal(t, 2, resp.ProtoMajor,
			"the client and proxy must negotiate HTTP/2 over the MITM'd connection")
	})

	t.Run("a response writer that does not support hijacking gets 500", func(t *testing.T) {
		p, err := proxy.New(newTestIntermediateCA(t), "kevin.home", nil, true)
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodConnect, "https://blocked.kevin.test:443", nil)
		req.Host = "blocked.kevin.test:443"
		rw := httptest.NewRecorder()

		p.ServeHTTP(rw, req)

		assert.Equal(t, http.StatusInternalServerError, rw.Code)
	})

	t.Run("a client that sends garbage instead of a TLS handshake is disconnected", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		_, err = io.WriteString(conn, "CONNECT blocked.kevin.test:443 HTTP/1.1\r\nHost: blocked.kevin.test:443\r\n\r\n")
		require.NoError(t, err)

		br := bufio.NewReader(conn)
		line, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Contains(t, line, "200")
		blank, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "\r\n", blank)

		_, err = conn.Write([]byte("not a tls client hello"))
		require.NoError(t, err)

		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		data, err := io.ReadAll(br)
		require.NoError(t, err, "the proxy must close the connection after a failed handshake, not hang")
		assert.Empty(t, data)
	})

	t.Run("a ClientHello folded into the CONNECT write is not lost", func(t *testing.T) {
		// A well-behaved client waits for "200 Connection Established" before sending its ClientHello.
		// However, a client MAY send both at once. In this case, net/http will buffer the ClientHello's leading bytes internally.
		// These buffered bytes will be handed to us in the Hijack response as a *bufio.ReadWriter.
		//
		// handleConnect must ensure that we forward these bytes from there.
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "coalesced")
		p.AddRoutes(proxy.Route{Host: "coalesced.kevin.test", Upstream: getTestURLHost(t, target.URL)})
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		pool := transport.TLSClientConfig.RootCAs

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		br := bufio.NewReader(conn)
		pc := &prefixOnceConn{
			Conn:   conn,
			br:     br,
			prefix: make(chan []byte, 1),
			ready:  make(chan struct{}),
		}
		tlsClient := tls.Client(pc, &tls.Config{
			RootCAs:    pool,
			ServerName: "coalesced.kevin.test",
			MinVersion: tls.VersionTLS12,
		})

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		hsErr := make(chan error, 1)
		go func() { hsErr <- tlsClient.HandshakeContext(ctx) }()

		// Capture the ClientHello tls.Client would normally write straight
		// to the wire, so it can be folded into the same write as the
		// CONNECT request instead of sent on its own afterward.
		clientHello := <-pc.prefix

		req := "CONNECT coalesced.kevin.test:443 HTTP/1.1\r\n" +
			"Host: coalesced.kevin.test:443\r\n\r\n"
		_, err = conn.Write(append([]byte(req), clientHello...))
		require.NoError(t, err)

		line, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Contains(t, line, "200")
		blank, err := br.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "\r\n", blank)
		close(pc.ready) // let tlsClient's Read see the rest of the handshake

		require.NoError(t, <-hsErr,
			"the handshake must complete even with the ClientHello folded into the CONNECT write")
		assert.Equal(t, "coalesced.kevin.test", tlsClient.ConnectionState().PeerCertificates[0].Subject.CommonName)
	})
}

// prefixOnceConn captures the first bytes written to it - a tls.Client's
// ClientHello - instead of sending them, so a test can fold those bytes into
// an earlier write of its own. Reads are held back until ready is closed, so
// nothing races the caller's own reads on the shared *bufio.Reader.
type prefixOnceConn struct {
	net.Conn

	br     *bufio.Reader
	prefix chan []byte
	ready  chan struct{}
	wrote  bool
}

func (c *prefixOnceConn) Write(p []byte) (int, error) {
	if !c.wrote { // captures the first write, and sends it to the prefix channel
		c.wrote = true
		c.prefix <- append([]byte(nil), p...)
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func (c *prefixOnceConn) Read(p []byte) (int, error) {
	<-c.ready
	return c.br.Read(p)
}
