package proxy_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/proxy"
)

// TestWebsocket covers serveUpgrade end to end: the handshake succeeding and
// piping bytes both directions afterward, and its error paths - a request
// the proxy cannot hijack, an upstream that never accepts the dial, and an
// upstream whose response the proxy cannot parse. None of the failure modes
// must hang, panic, or forward bytes the pipe never actually validated.
func TestWebsocket(t *testing.T) {
	t.Run("frames sent after the handshake completes", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(proxy.Route{Host: "ws.kevin.test", Upstream: wsEchoUpstream(t)})
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		// net/http.Client has no first-class WebSocket support, and testing
		// over plain HTTP through the proxy already exercises the same
		// forward/serveUpgrade path a wss:// request through CONNECT would.
		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		req := "GET http://ws.kevin.test/chat HTTP/1.1\r\n" +
			"Host: ws.kevin.test\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		_, err = io.WriteString(conn, req)
		require.NoError(t, err)

		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		assert.Equal(t, "websocket", resp.Header.Get("Upgrade"))

		const msg = "hello over the wire"
		_, err = io.WriteString(conn, msg)
		require.NoError(t, err)

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(br, buf)
		require.NoError(t, err)
		assert.Equal(t, msg, string(buf))
	})

	t.Run("bytes folded into the handshake write are not lost", func(t *testing.T) {
		// net/http can buffer bytes past the header boundary internally and
		// hand them back, still unread, in the *bufio.ReadWriter that Hijack
		// returns rather than leaving them on the wire. serveUpgrade must
		// forward bytes from there, not just from the raw hijacked net.Conn.
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(proxy.Route{Host: "wsbuf.kevin.test", Upstream: wsEchoUpstream(t)})
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		const msg = "hello over the wire"
		req := "GET http://wsbuf.kevin.test/chat HTTP/1.1\r\n" +
			"Host: wsbuf.kevin.test\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n" + msg
		_, err = io.WriteString(conn, req)
		require.NoError(t, err)

		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
		assert.Equal(t, "websocket", resp.Header.Get("Upgrade"))

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(br, buf)
		require.NoError(t, err)
		assert.Equal(t, msg, string(buf))
	})

	t.Run("a response writer that does not support hijacking gets 500", func(t *testing.T) {
		p, err := proxy.New(newTestIntermediateCA(t), "kevin.home", nil, true)
		require.NoError(t, err)
		p.AddRoutes(proxy.Route{Host: "ws.kevin.test", Upstream: "127.0.0.1:1"})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://ws.kevin.test/chat", nil)
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("the upstream cannot be dialed", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(proxy.Route{Host: "deadws.kevin.test", Upstream: "127.0.0.1:1"})
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		req := "GET http://deadws.kevin.test/chat HTTP/1.1\r\n" +
			"Host: deadws.kevin.test\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		_, err = io.WriteString(conn, req)
		require.NoError(t, err)

		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	t.Run("the upstream's response cannot be parsed", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(proxy.Route{Host: "broken.kevin.test", Upstream: brokenWSUpstream(t)})
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", p.Addr())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		req := "GET http://broken.kevin.test/chat HTTP/1.1\r\n" +
			"Host: broken.kevin.test\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n\r\n"
		_, err = io.WriteString(conn, req)
		require.NoError(t, err)

		// serveUpgrade gives up silently once it can't parse the upstream's
		// answer, so the client must see the connection close rather than
		// hang waiting for bytes that will never come.
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
		data, err := io.ReadAll(conn)
		require.NoError(t, err, "the proxy must close the connection, not hang")
		assert.Empty(t, data)
	})
}

// wsEchoUpstream answers an Upgrade request with 101 and echoes every byte
// it receives afterward - enough to prove the proxy's hijack-and-pipe path,
// without a real WebSocket framing library.
func wsEchoUpstream(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // test fixture, best effort

		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_ = req.Body.Close()

		const resp = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n"
		if _, err := io.WriteString(conn, resp); err != nil {
			return
		}
		_, _ = io.Copy(conn, br)
	}()
	return ln.Addr().String()
}

// brokenWSUpstream accepts one connection, reads the request, and answers
// with bytes that are not a valid HTTP response - enough to make
// http.ReadResponse fail on the proxy's side of the upgrade.
func brokenWSUpstream(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // test fixture, best effort

		br := bufio.NewReader(conn)
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		_ = req.Body.Close()
		_, _ = io.WriteString(conn, "not an http response\r\n\r\n")
	}()
	return ln.Addr().String()
}
