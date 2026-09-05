package proxy

import (
	"bufio"
	"io"
	"net/http"
	"time"
)

// tunnelRoute serves a CONNECT for a route whose upstream already speaks
// TLS and asked to skip kevin's MITM (Route.SkipMITM): bytes pass straight
// through to the upstream, undecrypted, so the client validates the
// upstream's own certificate instead of a kevin-signed leaf.
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
	p.recordRequest(r, target.Host, true, false, start, http.StatusOK)
}
