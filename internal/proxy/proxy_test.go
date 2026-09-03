package proxy_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/state"
)

func TestRoutes(t *testing.T) {
	t.Run("plain HTTP by host", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "from the workload")

		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		resp, body := getTestURL(t, client, "http://api.kevin.test/some/path")

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "from the workload", body)
		assert.Equal(t, "api.kevin.test", resp.Header.Get("X-Seen-Host"),
			"the workload must see the hostname that the client asked for")
	})

	t.Run("ignore case and port", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "matched")

		p.AddRoutes(proxy.Route{Host: "API.Kevin.Test", Upstream: getTestURLHost(t, target.URL)})

		_, body := getTestURL(t, client, "http://api.kevin.test/")
		assert.Equal(t, "matched", body)
	})

	t.Run("AddRoutes replaces an earlier route for the same host", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		first := createTestUpstream(t, "first")
		second := createTestUpstream(t, "second")

		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: getTestURLHost(t, first.URL)})
		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: getTestURLHost(t, second.URL)})

		_, body := getTestURL(t, client, "http://api.kevin.test/")
		assert.Equal(t, "second", body)
		assert.Len(t, p.Routes(), 1)
	})

	t.Run("an unreachable upstream returns bad gateway", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)

		// Port 1 on loopback is never listening, so the dial fails
		// immediately instead of timing out.
		p.AddRoutes(proxy.Route{Host: "dead.kevin.test", Upstream: "127.0.0.1:1"})

		resp, _ := getTestURL(t, client, "http://dead.kevin.test/")

		assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	})

	t.Run("a wildcard route matches a subdomain", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "matched a wildcard route")

		p.AddRoutes(proxy.Route{Host: "*.s3.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		_, body := getTestURL(t, client, "http://bucket.s3.kevin.test/")
		assert.Equal(t, "matched a wildcard route", body)
	})

	t.Run("a wildcard route does not match the bare domain", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)

		p.AddRoutes(proxy.Route{Host: "*.s3.kevin.test", Upstream: "127.0.0.1:1"})

		resp, _ := getTestURL(t, client, "http://s3.kevin.test/")
		assert.Equal(t, http.StatusForbidden, resp.StatusCode,
			"a wildcard must not match the bare domain it wraps")
	})

	t.Run("an exact route wins over an overlapping wildcard", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		exact := createTestUpstream(t, "exact")
		wildcard := createTestUpstream(t, "wildcard")

		p.AddRoutes(proxy.Route{Host: "*.kevin.test", Upstream: getTestURLHost(t, wildcard.URL)})
		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: getTestURLHost(t, exact.URL)})

		_, body := getTestURL(t, client, "http://api.kevin.test/")
		assert.Equal(t, "exact", body)
	})

	t.Run("the more specific of two overlapping wildcards wins", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		general := createTestUpstream(t, "general")
		specific := createTestUpstream(t, "specific")

		p.AddRoutes(proxy.Route{Host: "*.kevin.test", Upstream: getTestURLHost(t, general.URL)})
		p.AddRoutes(proxy.Route{Host: "*.s3.kevin.test", Upstream: getTestURLHost(t, specific.URL)})

		_, body := getTestURL(t, client, "http://bucket.s3.kevin.test/")
		assert.Equal(t, "specific", body)
	})

	t.Run("AddRoutes replaces an earlier route for the same wildcard host", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		first := createTestUpstream(t, "first")
		second := createTestUpstream(t, "second")

		p.AddRoutes(proxy.Route{Host: "*.kevin.test", Upstream: getTestURLHost(t, first.URL)})
		p.AddRoutes(proxy.Route{Host: "*.kevin.test", Upstream: getTestURLHost(t, second.URL)})

		_, body := getTestURL(t, client, "http://sub.kevin.test/")
		assert.Equal(t, "second", body)
		assert.Len(t, p.Routes(), 1)
	})
}

func TestEgress(t *testing.T) {
	t.Run("an allowed host reaches the upstream", func(t *testing.T) {
		target := createTestUpstream(t, "allowed")
		_, client := startTestProxyWithClientAndEgressFiltering(t, []string{"127.0.0.1"}, true)

		_, body := getTestURL(t, client, target.URL+"/")
		assert.Equal(t, "allowed", body)
	})

	t.Run("a registered route needs no allow entry", func(t *testing.T) {
		p, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "a workload")

		p.AddRoutes(proxy.Route{Host: "svc.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		resp, body := getTestURL(t, client, "http://svc.kevin.test/")

		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"a step of the environment is not egress")
		assert.Equal(t, "a workload", body)
	})

	t.Run("a wildcard allows a subdomain", func(t *testing.T) {
		// httptest binds its server to the IPv4 loopback, 127.0.0.1. A
		// wildcard match is a plain string suffix, so "*.0.0.1" exercises the
		// real proxy path against that address without any DNS lookup.
		_, client := startTestProxyWithClientAndEgressFiltering(t, []string{"*.0.0.1"}, true)
		target := createTestUpstream(t, "matched a subdomain")

		_, body := getTestURL(t, client, target.URL+"/")
		assert.Equal(t, "matched a subdomain", body)
	})

	t.Run("passes an unknown host through when deny is off", func(t *testing.T) {
		p, client := startTestProxyWithClientAndEgressFiltering(t, nil, false)
		target := createTestUpstream(t, "the real internet")

		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: "127.0.0.1:1"})

		// The httptest server stands in for a host that the proxy does not
		// route and does not allow. Deny is off, so the request still
		// reaches it.
		_, body := getTestURL(t, client, target.URL+"/")
		assert.Equal(t, "the real internet", body)
	})
}

func TestDeniedRequest(t *testing.T) {
	t.Run("an unknown host over plain HTTP", func(t *testing.T) {
		_, client := startTestProxyWithClient(t)
		target := createTestUpstream(t, "should never be seen")

		resp, body := getTestURL(t, client, target.URL+"/")

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, body, "Blocked by kevin")
		assert.NotContains(t, body, "should never be seen",
			"a denied request must never reach the upstream")
	})

	t.Run("an unknown host over HTTPS through connect", func(t *testing.T) {
		_, client := startTestProxyWithClient(t)

		resp, body := getTestURL(t, client, "https://denied.kevin.test/")

		require.NotNil(t, resp.TLS,
			"the proxy must MITM a denied host rather than reject the CONNECT")
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, body, "Blocked by kevin",
			"the client must see a readable page, not a TLS error")
		assert.Contains(t, body, "denied.kevin.test", "the page must name the denied host")
		assert.Contains(t, body, `proxy: egress: allow: ["denied.kevin.test"]`,
			"the page must show the CUE to allow the host for the whole environment")
		assert.Contains(t, body, `env: myStep: with: egress: ["denied.kevin.test"]`,
			"the page must show the CUE to allow the host for one step")
	})

	t.Run("the response is never cached", func(t *testing.T) {
		_, client := startTestProxyWithClient(t)

		resp, _ := getTestURL(t, client, "https://denied.kevin.test/")

		assert.Contains(t, resp.Header.Get("Cache-Control"), "no-store")
		assert.Empty(t, resp.Header.Get("ETag"),
			"a validator would let a client reuse the denial after the allow list changes")
		assert.Empty(t, resp.Header.Get("Last-Modified"))
	})

	t.Run("the response is plain text for a non-HTML client", func(t *testing.T) {
		_, client := startTestProxyWithClient(t)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://denied.kevin.test/", nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resp.Body.Close() })

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, resp.Header.Get("Content-Type"), "text/plain")
		assert.NotContains(t, string(body), "<html>",
			"a client that does not accept HTML must still get a readable body")
		assert.Contains(t, string(body), "Blocked by kevin")
	})
}

func TestEgressAllowed(t *testing.T) {
	p, _ := startTestProxyWithClientAndEgressFiltering(t, []string{"api.github.com", "*.example.com"}, true)

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "an exact match", host: "api.github.com", want: true},
		{name: "a wildcard match", host: "sub.example.com", want: true},
		{name: "a different case", host: "API.GITHUB.COM", want: true},
		{name: "a host with a port", host: "api.github.com:443", want: true},
		{name: "a wildcard does not match the bare domain", host: "example.com", want: false},
		{name: "an unknown host", host: "evil.test", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, p.EgressAllowed(tt.host))
		})
	}
}

func TestEgressAllowList(t *testing.T) {
	p, _ := startTestProxyWithClientAndEgressFiltering(t, []string{"api.github.com", "*.example.com"}, true)

	allow, wildcards, deny := p.EgressAllowList()

	assert.Equal(t, []string{"api.github.com"}, allow)
	assert.Equal(t, []string{".example.com"}, wildcards)
	assert.True(t, deny)
}

func TestLookup(t *testing.T) {
	p, _ := startTestProxyWithClient(t)
	p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: "api:8080"})

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "an exact match", host: "api.kevin.test", want: true},
		{name: "a host with a port", host: "api.kevin.test:443", want: true},
		{name: "a different case", host: "API.KEVIN.TEST", want: true},
		{name: "an unknown host", host: "other.kevin.test", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route, ok := p.Lookup(tt.host)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Equal(t, "api:8080", route.Upstream)
			}
		})
	}
}

func TestPAC(t *testing.T) {
	t.Run("sends the domain and every route through the proxy", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(
			proxy.Route{Host: "web.kevin.home", Upstream: "127.0.0.1:1"},
			proxy.Route{Host: "www.example.test", Upstream: "127.0.0.1:2"},
		)

		body := pac(t, p)

		assert.Contains(t, body, "function FindProxyForURL(url, host)")
		assert.Contains(t, body, `return "DIRECT"`, "normal browsing must not go through the proxy")

		// The whole environment domain matches on a suffix, thus a step added
		// later needs no reload of the file.
		assert.Contains(t, body, `dnsDomainIs(host, ".kevin.home")`)
		assert.NotContains(t, body, `host === "web.kevin.home"`,
			"a name under the domain is already covered by the suffix")

		// A name outside the domain needs its own line.
		assert.Contains(t, body, `host === "www.example.test"`)
	})

	t.Run("sends a wildcard route through dnsDomainIs, not an exact match", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		p.AddRoutes(proxy.Route{Host: "*.s3.example.test", Upstream: "127.0.0.1:1"})

		body := pac(t, p)

		assert.Contains(t, body, `dnsDomainIs(host, ".s3.example.test")`)
		assert.NotContains(t, body, `host === "*.s3.example.test"`,
			"the literal wildcard string is never a valid FindProxyForURL comparison")
	})

	t.Run("names the proxy by the host that was asked", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)

		// A browser reaches the file by some name. The answer must send
		// traffic back to that same name, not to whatever the listener bound
		// to.
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, proxy.PACPath, nil)
		req.Host = "kevin.local:9999"
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"PROXY kevin.local:9999"`)
	})

	t.Run("is served with the right content type", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, proxy.PACPath, nil)
		req.Host = "127.0.0.1:8080"
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		assert.Equal(t, "application/x-ns-proxy-autoconfig", rec.Header().Get("Content-Type"))
	})

	t.Run("falls back to Addr when the request carries no Host", func(t *testing.T) {
		p, _ := startTestProxyWithClient(t)
		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, proxy.PACPath, nil)
		req.Host = ""
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), `"PROXY `+p.Addr()+`"`)
	})

	t.Run("answers 503 before Serve has bound a listener", func(t *testing.T) {
		p, err := proxy.New(newTestIntermediateCA(t), "kevin.home", nil, true)
		require.NoError(t, err)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, proxy.PACPath, nil)
		req.Host = ""
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

// TestServe covers Proxy's serving lifecycle: ServeHTTP works standalone
// with no listener ever bound, and once Serve is given more than one
// listener, Addr reports the first while every listener routes the same.
func TestServe(t *testing.T) {
	t.Run("ServeHTTP handles a request without a bound listener", func(t *testing.T) {
		authority := newTestIntermediateCA(t)

		p, err := proxy.New(authority, "kevin.home", nil, true)
		require.NoError(t, err)

		target := createTestUpstream(t, "direct")
		p.AddRoutes(proxy.Route{Host: "api.kevin.test", Upstream: getTestURLHost(t, target.URL)})

		// A proxied request carries an absolute URI.
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://api.kevin.test/", nil)
		rec := httptest.NewRecorder()

		p.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "direct", rec.Body.String())
	})

	t.Run("reports the first of several listeners as Addr, and serves routes on every listener", func(t *testing.T) {
		authority := newTestIntermediateCA(t)
		p, err := proxy.New(authority, "kevin.home", nil, false)
		require.NoError(t, err)

		var lc net.ListenConfig
		first, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		second, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- p.Serve(ctx, first, second) }()
		t.Cleanup(func() {
			cancel()
			require.NoError(t, <-done)
		})

		assert.Eventually(t, func() bool { return p.Addr() != "" }, time.Second, time.Millisecond)
		assert.Equal(t, first.Addr().String(), p.Addr(), "Addr must report the first listener")

		target := createTestUpstream(t, "second listener works")
		p.AddRoutes(proxy.Route{Host: "second.kevin.home", Upstream: getTestURLHost(t, target.URL)})

		secondURL, err := url.Parse("http://" + second.Addr().String())
		require.NoError(t, err)
		client := &http.Client{
			Timeout:   5 * time.Second,
			Transport: &http.Transport{Proxy: http.ProxyURL(secondURL)},
		}

		resp, body := getTestURL(t, client, "http://second.kevin.home/")
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "second listener works", body, "the second listener must serve the same routes")
	})
}

func TestOnRecord(t *testing.T) {
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
	snapshot := func() []proxy.Record {
		mu.Lock()
		defer mu.Unlock()
		return append([]proxy.Record(nil), records...)
	}

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
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: authority.Pool(), MinVersion: tls.VersionTLS12},
		},
	}

	target := createTestUpstream(t, "recorded")
	p.AddRoutes(proxy.Route{Host: "recorded.kevin.test", Upstream: getTestURLHost(t, target.URL)})

	getTestURL(t, client, "http://recorded.kevin.test/some/path")
	getTestURL(t, client, "http://denied.kevin.test/")

	require.Eventually(t, func() bool { return len(snapshot()) == 2 }, time.Second, time.Millisecond)
	got := snapshot()

	routed := got[0]
	assert.Equal(t, http.MethodGet, routed.Method)
	assert.Equal(t, "recorded.kevin.test", routed.Host)
	assert.Equal(t, "/some/path", routed.Path)
	assert.Equal(t, http.StatusOK, routed.Status)
	assert.True(t, routed.Routed)
	assert.False(t, routed.Denied)
	assert.False(t, routed.Time.IsZero())

	denied := got[1]
	assert.Equal(t, "denied.kevin.test", denied.Host)
	assert.Equal(t, http.StatusForbidden, denied.Status)
	assert.False(t, denied.Routed)
	assert.True(t, denied.Denied)
}

// pac fetches the proxy auto-config file.
func pac(t *testing.T, p *proxy.Proxy) string {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, proxy.PACPath, nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// startTestProxyWithClient builds a proxy and an http client with egress filtering disabled.
func startTestProxyWithClient(t *testing.T) (*proxy.Proxy, *http.Client) {
	t.Helper()
	return startTestProxyWithClientAndEgressFiltering(t, nil, true)
}

// startTestProxyWithClientAndEgressFiltering builds a proxy and an http client with egress filtering enabled.
// allow is the list of hosts that are allowed to be accessed through the proxy.
// deny is the toggle for egress filtering. If it is false, no egress filtering is applied.
func startTestProxyWithClientAndEgressFiltering(t *testing.T, allow []string, deny bool) (*proxy.Proxy, *http.Client) {
	t.Helper()
	return startTestProxyServingWithAuthority(t, newTestIntermediateCA(t), allow, deny)
}

// startTestProxyServingWithAuthority is [startTestProxyWithClientAndEgressFiltering],
// against a caller-supplied authority - for a test that also needs the same
// authority itself, to mint a leaf certificate for a fake TLS upstream.
func startTestProxyServingWithAuthority(t *testing.T, authority *ca.CA, allow []string, deny bool) (*proxy.Proxy, *http.Client) {
	t.Helper()

	p, err := proxy.New(authority, "kevin.home", allow, deny)
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

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: authority.Pool(), MinVersion: tls.VersionTLS12},
		},
	}
	return p, client
}

// upstream is a server that reports the Host header it received.
func createTestUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-Host", r.Host)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testGetURL returns the response from a GET request to the given URL.
func getTestURL(t *testing.T, client *http.Client, rawURL string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp, string(body)
}

func getTestURLHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Host
}

// newIntermediateCA builds a root and the intermediate of one project.
func newTestIntermediateCA(t *testing.T) *ca.CA {
	t.Helper()

	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())

	m := ca.NewManager("cwd", "", "proxy-test", ca.Options{})
	_, err := m.LoadOrGenerateRoot()
	require.NoError(t, err)

	authority, err := m.LoadOrGenerateIntermediate()
	require.NoError(t, err)
	return authority
}
