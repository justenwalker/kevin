// Package proxy serves the three roles of the kevin proxy on one or more
// listeners.
//
//	p, err := proxy.New(authority, "kevin.home", allow, true)
//	go p.Serve(ctx, listener)
//	p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: "api:8080"})
//
// A client points HTTP_PROXY and HTTPS_PROXY at the listener. The proxy then
//
//  1. intercepts TLS with a leaf that the kevin CA signs,
//  2. forwards a request whose Host matches a route to the workload,
//  3. denies any other request that the allow list omits, when deny is on,
//  4. passes every other request to the real internet.
//
// kevin changes no file on the host: a hostname resolves in the routing table
// of the proxy, not in DNS.
package proxy

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/sync/errgroup"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/logging"
)

var log = logging.New("proxy")

// schemeHTTPS is the URL scheme forward sets when a route's upstream itself
// speaks TLS.
const schemeHTTPS = "https"

// readHeaderTimeout bounds a client that opens a connection and sends nothing.
const readHeaderTimeout = 30 * time.Second

// idleTimeout closes a MITM'd HTTP/2 tunnel that sits open with no active
// stream, so a client that never closes its end doesn't leak the connection
// for the life of the proxy.
const idleTimeout = 90 * time.Second

// Record is one request that passed through the proxy.
type Record struct {
	// Time is when the proxy finished the request.
	Time time.Time

	Method string
	Host   string
	Path   string
	Status int

	// Duration is how long the upstream took to answer.
	Duration time.Duration

	// Routed is true when the request reached a workload rather than the
	// internet.
	Routed bool

	// Denied is true when the proxy blocked the request instead of forwarding
	// it.
	Denied bool
}

// Route sends every request for Host to Upstream.
type Route struct {
	// Host is the hostname that a client asks for. The match ignores the port
	// and the case.
	Host string

	// Upstream is the address to forward to, such as "api:8080". The address
	// must be reachable from the process that runs the proxy.
	Upstream string

	// TLS is true when the upstream itself speaks TLS.
	TLS bool
}

// Proxy is the kevin proxy. A Proxy is safe for concurrent use.
type Proxy struct {
	certs *certSigner
	rp    *httputil.ReverseProxy
	mux   *http.ServeMux
	h2srv *http2.Server

	rootPool *x509.CertPool

	// domain is the base name of the environment. The proxy.pac sends this
	// domain and every route through the proxy.
	domain string

	// addr is where the proxy listens. Serve fills it in, and proxy.pac needs
	// it to name the proxy to a browser.
	addr atomic.Pointer[string]

	// deny is true when the proxy blocks a host that no route and no allow
	// entry covers. New sets it once, and it never changes after.
	deny bool

	mu             sync.RWMutex
	routes         map[string]Route
	routeWildcards map[string]Route
	allow          map[string]struct{}
	wildcards      []string

	// onRecord receives one call for each request. It runs on the goroutine
	// of the request, thus it must not block.
	onRecord func(Record)
}

// PACPath is where the proxy serves its proxy auto-config file. Point a
// browser at http://<proxy addr>/proxy.pac.
const PACPath = "/proxy.pac"

// New builds a proxy that signs its leaf certificates with authority. Requests
// for the environment domain and for a registered route go through it.
//
// allow lists the hosts that every step may reach, in addition to the hosts
// that [Proxy.AllowEgress] adds later. When deny is true, the proxy blocks a
// host that no route and no allow entry covers.
func New(authority *ca.CA, domain string, allow []string, deny bool) (*Proxy, error) {
	signer, err := authority.TLSCertificate()
	if err != nil {
		return nil, err
	}
	certs, err := newCertSigner(signer)
	if err != nil {
		return nil, err
	}

	// x509.SystemCertPool returns a fresh clone, safe to extend in place -
	// it never affects the process-wide default pool. A platform that
	// reports no system pool (err or nil) still gets a pool of its own,
	// with the kevin root as the only anchor.
	rootPool, err := x509.SystemCertPool()
	if err != nil || rootPool == nil {
		rootPool = x509.NewCertPool()
	}
	rootPool.AppendCertsFromPEM([]byte(authority.RootPEM()))

	p := &Proxy{
		certs:          certs,
		rootPool:       rootPool,
		domain:         domain,
		deny:           deny,
		routes:         map[string]Route{},
		routeWildcards: map[string]Route{},
		allow:          map[string]struct{}{},
		h2srv:          &http2.Server{IdleTimeout: idleTimeout},
	}
	p.AllowEgress(allow...)

	// A no-op Rewrite: forward already rewrites r.URL (scheme, host) to the
	// real target before handing the request to rp, so there is nothing left
	// for Rewrite itself to do.
	//
	// Transport clones http.DefaultTransport but dials through p.dialContext
	// and completes TLS itself through p.dialTLSContext, so a route whose
	// Upstream is a socks5:// relay address (see relay.go) gets CONNECTed
	// through the relay instead of dialed directly, and a tls: true route's
	// certificate is verified against the real upstream hostname (from the
	// request context) rather than the dial address, which for a relay
	// route is the relay's own address, not the upstream's identity.
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("proxy: http.DefaultTransport is %T, not *http.Transport", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.DialContext = p.dialContext
	transport.DialTLSContext = p.dialTLSContext
	p.rp = &httputil.ReverseProxy{Rewrite: func(*httputil.ProxyRequest) {}, Transport: transport}

	// A browser fetches the proxy auto-config file directly, not through the
	// proxy, thus it arrives with a path and no absolute URI.
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PACPath, p.pac)
	p.mux = mux

	return p, nil
}

// OnRecord registers a function that receives one Record for each request.
// Call it before Serve.
func (p *Proxy) OnRecord(fn func(Record)) { p.onRecord = fn }

// AddRoutes puts every route into the table. A route replaces an earlier route
// for the same host. A Host with a leading "*." wildcard, such as
// "*.s3.amazonaws.com", matches any subdomain the same way a wildcard entry
// in [Proxy.AllowEgress] does - it does not match the bare domain.
func (p *Proxy) AddRoutes(routes ...Route) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range routes {
		host := strings.ToLower(r.Host)
		if suffix, ok := strings.CutPrefix(host, "*."); ok {
			p.routeWildcards["."+suffix] = r
			continue
		}
		p.routes[host] = r
	}
}

// AllowEgress adds hosts to the allow list. A host is an exact name or a
// leading-dot wildcard, such as "*.github.com". A wildcard matches a
// subdomain, such as "api.github.com". A wildcard does not match the bare
// name "github.com". List the bare name too when both need to reach the
// internet. Matching ignores case and ignores any port.
func (p *Proxy) AllowEgress(hosts ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			p.wildcards = append(p.wildcards, "."+suffix)
			continue
		}
		p.allow[h] = struct{}{}
	}
}

// EgressAllowed reports whether host is on the allow list, exactly or through
// a wildcard. It does not check the routing table and does not check the
// deny toggle.
func (p *Proxy) EgressAllowed(host string) bool {
	host = strings.ToLower(hostOnly(host))

	p.mu.RLock()
	defer p.mu.RUnlock()

	if _, ok := p.allow[host]; ok {
		return true
	}
	for _, suffix := range p.wildcards {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// EgressAllowList returns the allow list as configured: exact hosts and
// leading-dot wildcards, separately, plus whether the proxy denies by
// default. It does not consult the routing table.
func (p *Proxy) EgressAllowList() ([]string, []string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	allow := make([]string, 0, len(p.allow))
	for h := range p.allow {
		allow = append(allow, h)
	}
	sort.Strings(allow)
	wildcards := append([]string(nil), p.wildcards...)
	sort.Strings(wildcards)
	return allow, wildcards, p.deny
}

// Routes returns the table, exact hosts and wildcards alike.
func (p *Proxy) Routes() []Route {
	p.mu.RLock()
	defer p.mu.RUnlock()

	routes := make([]Route, 0, len(p.routes)+len(p.routeWildcards))
	for _, r := range p.routes {
		routes = append(routes, r)
	}
	for _, r := range p.routeWildcards {
		routes = append(routes, r)
	}
	return routes
}

// Lookup returns the route for a host. The host may carry a port. An exact
// match wins; otherwise the most specific (longest) matching wildcard wins.
func (p *Proxy) Lookup(host string) (Route, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	h := strings.ToLower(hostOnly(host))
	if r, ok := p.routes[h]; ok {
		return r, true
	}

	var (
		best      Route
		bestLen   int
		bestFound bool
	)
	for suffix, r := range p.routeWildcards {
		if strings.HasSuffix(h, suffix) && len(suffix) > bestLen {
			best, bestLen, bestFound = r, len(suffix), true
		}
	}
	return best, bestFound
}

// ServeHTTP handles one proxied request: a CONNECT is hijacked and MITM'd, a
// request with no absolute URI is a direct hit on the proxy's own address
// (the PAC file), and everything else is a proxied request to route, deny,
// or forward.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodConnect:
		p.handleConnect(w, r)
	case r.URL.Host == "":
		p.mux.ServeHTTP(w, r)
	default:
		p.forward(w, r)
	}
}

// Serve runs the proxy on every listener until ctx ends. Serve reports
// [Proxy.Addr] as the address of the first listener: a caller that binds a
// second listener for use inside a docker network passes it after the host
// listener.
func (p *Proxy) Serve(ctx context.Context, listeners ...net.Listener) error {
	if len(listeners) == 0 {
		return nil
	}

	addr := listeners[0].Addr().String()
	p.addr.Store(&addr)

	srv := &http.Server{
		Handler:           p,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	grp, _ := errgroup.WithContext(ctx)
	for _, ln := range listeners {
		grp.Go(func() error {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("proxy: serve: %w", err)
			}
			return nil
		})
	}
	return grp.Wait() //nolint:wrapcheck // each listener's error is already wrapped inside its own goroutine; wrapping again here would double it
}

// pac serves a proxy auto-config file. A browser that loads it sends the
// environment domain and every registered route through the proxy, and reaches
// everything else directly.
func (p *Proxy) pac(w http.ResponseWriter, r *http.Request) {
	// Answer with the host that the browser asked for. A proxy on the
	// loopback and one on a LAN address then both work, and the file needs no
	// knowledge of how the listener bound.
	addr := r.Host
	if addr == "" {
		addr = p.Addr()
	}
	if addr == "" {
		http.Error(w, "the proxy is not listening yet", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, p.pacScript(addr))
}

// pacScript builds the proxy auto-config file.
func (p *Proxy) pacScript(addr string) string {
	var hosts, wildcards []string
	for _, r := range p.Routes() {
		if p.domain != "" && strings.HasSuffix(r.Host, "."+p.domain) {
			continue
		}
		if suffix, ok := strings.CutPrefix(strings.ToLower(r.Host), "*."); ok {
			wildcards = append(wildcards, "."+suffix)
			continue
		}
		hosts = append(hosts, strings.ToLower(r.Host))
	}
	sort.Strings(hosts)
	sort.Strings(wildcards)

	var b strings.Builder
	b.WriteString("// Generated by kevin. Reload to pick up new steps.\n")
	b.WriteString("function FindProxyForURL(url, host) {\n")
	b.WriteString("  host = host.toLowerCase();\n")
	b.WriteString("  var proxy = \"PROXY " + addr + "\";\n")

	if p.domain != "" {
		b.WriteString("  if (host === " + quoteJS(p.domain) +
			" || dnsDomainIs(host, " + quoteJS("."+p.domain) + ")) return proxy;\n")
	}
	for _, h := range hosts {
		b.WriteString("  if (host === " + quoteJS(h) + ") return proxy;\n")
	}
	for _, suffix := range wildcards {
		b.WriteString("  if (dnsDomainIs(host, " + quoteJS(suffix) + ")) return proxy;\n")
	}

	b.WriteString("  return \"DIRECT\";\n")
	b.WriteString("}\n")
	return b.String()
}

// quoteJS renders a string as a JavaScript literal.
func quoteJS(s string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
	return `"` + escaped + `"`
}

// Addr is where the proxy listens. It is empty until Serve runs.
func (p *Proxy) Addr() string {
	if a := p.addr.Load(); a != nil {
		return *a
	}
	return ""
}

// forward routes r: it rewrites the request and forwards it to a matching
// workload, denies a host that no route and no allow entry covers when deny
// is on, or passes everything else to the real internet. It records the
// outcome.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	target, routed := p.Lookup(r.URL.Host)
	host := hostOnly(r.URL.Host)
	if host == "" {
		host = hostOnly(r.Host)
	}

	if !routed && p.deny && !p.EgressAllowed(host) {
		log.Ctx(r.Context()).Debug("denied", "host", host)
		writeForbidden(w, r, host)
		p.recordRequest(r, host, false, true, start, http.StatusForbidden)
		return
	}

	if routed {
		// Keep the original Host header. The workload sees the name that the
		// client asked for, and a virtual host on the upstream still works.
		upstream := target.Upstream
		if relay, connectTarget, ok := splitSOCKS5(upstream); ok {
			// Upstream names a relay-reachable target, not something this
			// process can dial directly (see relay.go). r.URL.Host stays the
			// real target - never the relay's own address - so Transport's
			// keep-alive pool (keyed off the request URL) can't confuse two
			// routes that share one relay; carry the relay itself on the
			// context for dialContext to CONNECT through.
			upstream = connectTarget
			r = r.WithContext(withSOCKS5Relay(r.Context(), relay))
		}
		r.URL.Host = upstream
		r.URL.Scheme = "http"
		if target.TLS {
			r.URL.Scheme = schemeHTTPS
			// Carry the name the client actually asked for on the context,
			// for dialTLSContext to verify the certificate against - r.URL.Host
			// is the target address, not necessarily a name a cert would list.
			r = r.WithContext(withTLSServerName(r.Context(), hostOnly(r.Host)))
		}
		host = hostOnly(r.Host)
		log.Ctx(r.Context()).Debug("routed", "host", target.Host, "upstream", target.Upstream)
	}

	if isUpgrade(r) {
		status := p.serveUpgrade(w, r)
		p.recordRequest(r, host, routed, false, start, status)
		return
	}

	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	p.rp.ServeHTTP(sw, r)
	p.recordRequest(r, host, routed, false, start, sw.status)
}

// statusWriter captures the status code a handler wrote, so a caller that
// wraps a plain http.ResponseWriter (such as httputil.ReverseProxy, which
// writes directly to it) can still learn what was sent for logging.
type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// recordRequest reports one finished request.
func (p *Proxy) recordRequest(r *http.Request, host string, routed, denied bool, start time.Time, status int) {
	if p.onRecord == nil {
		return
	}
	rec := Record{
		Time:   time.Now(),
		Method: r.Method,
		Host:   host,
		Path:   r.URL.Path,
		Status: status,
		Routed: routed,
		Denied: denied,
	}
	if !start.IsZero() {
		rec.Duration = time.Since(start)
	}
	p.onRecord(rec)
}

// deniedText is the plain-text body of a denial page. deniedHTML wraps the
// same message in a readable page.
const deniedText = `Blocked by kevin

The request to %[1]s did not reach the internet.

Egress is deny by default. To allow this host, add it to the environment:

    proxy: egress: allow: [%[1]q]

Or allow it for one step:

    env: myStep: with: egress: [%[1]q]
`

const deniedHTML = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Blocked by kevin</title></head>
<body>
<h1>Blocked by kevin</h1>
<p>kevin blocked a request to <code>%[1]s</code>.</p>
<p>Egress is deny by default. To allow this host, add it to the environment:</p>
<pre><code>proxy: egress: allow: [%[1]q]</code></pre>
<p>Or allow it for one step:</p>
<pre><code>env: myStep: with: egress: [%[1]q]</code></pre>
</body>
</html>
`

// writeForbidden writes the 403 page for a denied host. It renders HTML for a
// client that accepts HTML, and plain text otherwise. The response carries no
// cache headers. A browser must not reuse it once the user fixes the allow
// list.
func writeForbidden(w http.ResponseWriter, r *http.Request, host string) {
	contentType := "text/plain; charset=utf-8"
	body := fmt.Sprintf(deniedText, host)
	if acceptsHTML(r) {
		contentType = "text/html; charset=utf-8"
		body = fmt.Sprintf(deniedHTML, html.EscapeString(host))
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.WriteHeader(http.StatusForbidden)
	_, _ = io.WriteString(w, body) //nolint:gosec // body is fmt.Sprintf'd from a fixed template; the html.EscapeString above already covers the one templated value in the HTML case
}

// acceptsHTML reports whether r names text/html, text/*, or */* in its Accept
// header, or sends no Accept header at all.
func acceptsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	if accept == "" {
		return true
	}
	for part := range strings.SplitSeq(accept, ",") {
		mediaType, _, _ := strings.Cut(part, ";")
		switch strings.TrimSpace(mediaType) {
		case "text/html", "text/*", "*/*":
			return true
		}
	}
	return false
}

// hostOnly strips the port from a host.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
