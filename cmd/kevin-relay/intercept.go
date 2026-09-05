package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"golang.org/x/sync/errgroup"

	"github.com/justenwalker/kevin/internal/httpserver"
)

// interceptRequest is the body a control call POSTs to add a host. Mirrors
// internal/relay's identical type - not importable here, the relay binary
// sits above internal/relay in the dependency graph.
type interceptRequest struct {
	Host  string `json:"host"`
	Ports []int  `json:"ports"`
}

// serveControl runs the intercept control endpoint until ctx is done. A
// registration adds host to p.intercept's DNS matcher, then opens whichever
// of ports isn't already served by a listener - grp, so a newly-opened
// listener's accept loop joins the same lifecycle as everything else run
// already started.
func (p *relayProcess) serveControl(ctx context.Context, grp *errgroup.Group) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /intercept", func(w http.ResponseWriter, r *http.Request) {
		p.handleIntercept(ctx, grp, w, r)
	})
	return httpserver.Serve(ctx, p.controlLn, mux)
}

// handleIntercept decodes one interceptRequest and applies it.
func (p *relayProcess) handleIntercept(ctx context.Context, grp *errgroup.Group, w http.ResponseWriter, r *http.Request) {
	var req interceptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p.intercept.AddIntercept(req.Host)
	for _, port := range req.Ports {
		if err := p.ensureListener(ctx, grp, port); err != nil {
			log.Ctx(ctx).Debug("relay: intercept: open listener failed", "error", err, "port", port)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ensureListener opens a listener on port and serves it via grp, unless
// port is already served - by the fixed :80/:443 listeners, or by an
// earlier ensureListener call for the same port.
func (p *relayProcess) ensureListener(ctx context.Context, grp *errgroup.Group, port int) error {
	if port == 80 || port == 443 {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.extraLns == nil {
		p.extraLns = make(map[int]net.Listener)
	}
	if _, ok := p.extraLns[port]; ok {
		return nil
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("relay: listen intercept port %d: %w", port, err)
	}
	p.extraLns[port] = ln
	grp.Go(func() error { return serveIntercept(ctx, ln, p.proxyAddr, port) })
	return nil
}

// serveIntercept accepts connections on ln - bound for an external route's
// declared port beyond the fixed :80/:443 pair - and dispatches each to
// handleHTTPS or handleHTTP depending on whether its first byte looks like
// a TLS handshake record.
func serveIntercept(ctx context.Context, ln net.Listener, proxyAddr string, port int) error {
	return acceptLoop(ctx, ln, func(conn net.Conn) {
		br := bufio.NewReader(conn)
		first, err := br.Peek(1)
		if err != nil {
			_ = conn.Close()
			return
		}

		pc := &peekedConn{Conn: conn, r: br}
		if first[0] == recordTypeHandshake {
			handleHTTPS(ctx, pc, proxyAddr, port)
			return
		}
		handleHTTP(ctx, pc, proxyAddr)
	})
}

// peekedConn is a net.Conn whose Read replays r's buffered bytes before
// falling through to the underlying connection, so peeking a byte to
// decide a protocol doesn't lose it for the handler that runs next.
type peekedConn struct {
	net.Conn

	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err != nil {
		return n, fmt.Errorf("relay: read: %w", err)
	}
	return n, nil
}
