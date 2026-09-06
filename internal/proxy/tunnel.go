package proxy

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"time"
)

// tunnelRoute serves a CONNECT for a route in RouteModePassthrough or
// RouteModeRaw: bytes pass straight through to the upstream, undecrypted
// and unparsed, so a passthrough client validates the upstream's own
// certificate instead of a kevin-signed leaf, and a raw client's
// non-HTTP(S) protocol reaches it unmodified.
func (p *Proxy) tunnelRoute(w http.ResponseWriter, r *http.Request, target Route) {
	start := time.Now()

	dialAddr := target.Upstream
	ctx := r.Context()
	if relay, connectTarget, ok := splitSOCKS5(dialAddr); ok {
		// target.Upstream names a relay-reachable target, not something this
		// process can dial directly - dialAddr stays the real target, and the
		// relay itself goes on the context for dialContext to CONNECT through.
		dialAddr = connectTarget
		ctx = withSOCKS5Relay(ctx, relay)
	}

	upstream, err := p.dialContext(ctx, "tcp", dialAddr)
	if err != nil {
		log.Ctx(r.Context()).Debug("tunnel dial failed", "host", target.Host, "upstream", dialAddr, "error", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	p.tunnelPipe(w, r, upstream, target.Host, true, start)
}

// tunnelUnrouted serves a CONNECT for an unrouted host under
// proxy.egress.passthrough: bytes pass straight through to host, undecrypted,
// the same as tunnelRoute above, but dialing host directly instead of a
// configured Route.Upstream - an unrouted host is always a real internet
// address, never a socks5:// route upstream, so there is nothing to resolve
// first. The caller (handleConnect) has already cleared allow/deny.
func (p *Proxy) tunnelUnrouted(w http.ResponseWriter, r *http.Request, host string) {
	start := time.Now()

	upstream, err := p.dialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		log.Ctx(r.Context()).Debug("tunnel dial failed", "host", host, "error", err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	p.tunnelPipe(w, r, upstream, host, false, start)
}

// tunnelPipe answers r's CONNECT with 200, then splices bytes between the
// hijacked client connection and upstream until either side closes. Shared
// by tunnelRoute and tunnelUnrouted, which differ only in how they resolve
// the dial target and what they record.
func (p *Proxy) tunnelPipe(w http.ResponseWriter, r *http.Request, upstream net.Conn, recordHost string, routed bool, start time.Time) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "connect not supported", http.StatusInternalServerError)
		return
	}
	client, crw, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		log.Ctx(r.Context()).Debug("tunnel hijack failed", "error", err)
		return
	}
	defer client.Close()   //nolint:errcheck // best effort once the pipe (or an earlier error) ends
	defer upstream.Close() //nolint:errcheck // best effort once the pipe (or an earlier error) ends

	if _, err = io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	pipeUpgrade(client, upstream, bufio.NewReader(upstream), crw.Reader)
	p.recordRequest(r, recordHost, routed, false, start, http.StatusOK)
}
