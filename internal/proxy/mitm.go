package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"

	"golang.org/x/net/http2"
)

// handleConnect handles the HTTPS proxy CONNECT request.
//
// It hijacks the connection and terminates TLS with a leaf it mints on the fly.
// It also serves whatever http version the client negotiates: HTTP/2 or HTTP/1.1 over ALPN.
// Requests are forwarded to the matching workload.
//
// A route whose upstream already speaks TLS may ask to skip this and be
// tunneled through raw instead - see tunnelRoute.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)

	if target, routed := p.Lookup(host); routed && target.TLS && target.SkipMITM {
		p.tunnelRoute(w, r, target)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connect not supported", http.StatusInternalServerError)
		return
	}
	conn, crw, err := hj.Hijack()
	if err != nil {
		log.Ctx(r.Context()).Debug("hijack failed", "error", err)
		return
	}

	if _, err = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}

	// Read the ClientHello through crw.Reader, not conn directly: Hijack may
	// have already buffered bytes past the CONNECT request's header
	// boundary, and reading from conn would skip them.
	tlsConn := tls.Server(&bufferedConn{Conn: conn, r: crw.Reader}, &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.certs.leafFor(host)
		},
	})
	if err = tlsConn.HandshakeContext(r.Context()); err != nil {
		log.Ctx(r.Context()).Debug("tls handshake failed", "host", host, "error", err)
		_ = tlsConn.Close()
		return
	}

	handler := mitmConnect(host, http.HandlerFunc(p.forward))

	switch tlsConn.ConnectionState().NegotiatedProtocol {
	case "h2":
		p.h2srv.ServeConn(tlsConn, &http2.ServeConnOpts{Handler: handler})
	default: // "http/1.1", or no ALPN at all
		l := newSingleConnListener(tlsConn)
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}
		_ = srv.Serve(l)
	}
}

// bufferedConn is a net.Conn whose Read drains bytes Hijack already buffered
// ahead of the connection's boundary before falling through to the raw conn.
type bufferedConn struct {
	net.Conn

	r *bufio.Reader
}

//nolint:wrapcheck // net.Conn.Read must return io.EOF unwrapped: callers such as crypto/tls compare err == io.EOF, which a wrap would break.
func (b *bufferedConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// mitmConnect is a middleware that handles CONNECT requests for a given host.
// It overrides the URL scheme to HTTPS and sets URL Host to the CONNECT host.
func mitmConnect(host string, next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.URL.Scheme = schemeHTTPS
		if r.URL.Host == "" {
			r.URL.Host = host
		}
		next.ServeHTTP(w, r)
	}
}

// singleConnListener is a net.Listener for one already-open connection.
// This is required so the TLS'd conn from Hijack can hand off to a real http.Server for HTTP/1.1 parsing/keep-alive
// instead of hand-rolling that.
type singleConnListener struct {
	conn net.Conn
	addr net.Addr
}

func newSingleConnListener(conn net.Conn) *singleConnListener {
	return &singleConnListener{conn: conn, addr: conn.LocalAddr()}
}

// Accept accepts the single connection the listener was created with.
// Further calls will return io.EOF.
// This allows the server to shut down after the connection is closed.
func (l *singleConnListener) Accept() (net.Conn, error) {
	if l.conn == nil {
		return nil, io.EOF
	}
	c := l.conn
	l.conn = nil
	return c, nil
}

func (l *singleConnListener) Close() error   { return nil }
func (l *singleConnListener) Addr() net.Addr { return l.addr }
