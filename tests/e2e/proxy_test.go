//go:build e2e

package e2e

import (
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/suite"
)

// webCUE brings up a real nginx container reachable at web.kevin.home
// through the proxy, and a noproxy container that reaches it only through
// the docker network (proxy: false) - matches examples/web, minus the
// hardcoded ports (127.0.0.1:0 picks a free one).
const webCUE = `project: "%s"

env: {
	web: {
		uses:  "builtin:container"
		label: "Web Server"
		with: {
			image:  "nginx:alpine"
			expose: web: {port: 80}
		}
	}
	web_route: {
		uses:  "builtin:route"
		label: "Web Route"
		needs: ["web"]
		with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
	}
	noproxy: {
		uses:  "builtin:container"
		label: "No Proxy"
		needs: ["web", "web_route"]
		with: {
			proxy: false
			image: "busybox:stable"
			// web reporting ready only means its published port accepted one
			// TCP connection - a moment later the very next dial can still
			// see a transient refusal under load, so retry a few times
			// rather than fail the whole step on one flaky attempt.
			cmd: ["sh", "-c", "for i in 1 2 3 4 5 6 7 8 9 10; do wget -qO- http://web.kevin.home/ && break; sleep 1; done && sleep 3600"]
		}
	}
}
`

var addrRE = regexp.MustCompile(`(?m)^  (console|proxy)\s+http://(\S+)$`)

// ProxySuite covers docs/MANUAL_TESTING.md sections 2 (TLS termination,
// routing, NO_PROXY) and 3 (egress control). SetupSuite brings up one
// web-style project once, shared read-only across the TLS/PAC/deny tests;
// the egress-allow and egress-deny-false variants each need their own
// config, so they run their own project per test.
type ProxySuite struct {
	e2eSuite

	dir       string
	proxyAddr string
	rootCAs   *x509.CertPool
}

func TestProxySuite(t *testing.T) {
	suite.Run(t, new(ProxySuite))
}

func (s *ProxySuite) SetupSuite() {
	s.requireDocker()

	const project = "kevin-e2e-proxy"
	s.dir = s.project(project, webCUE)

	p := s.startKevin(s.dir, "-C", s.dir, "run")
	s.waitFor(p, stepLine("noproxy", "ready"), defaultTimeout)
	out := p.buf.String()

	m := addrRE.FindAllStringSubmatch(out, -1)
	s.Require().NotEmpty(m, "must print the console/proxy addresses, output:\n%s", out)
	for _, row := range m {
		if row[1] == "proxy" {
			s.proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(s.proxyAddr, "must find the proxy address, output:\n%s", out)

	pem, err := os.ReadFile(filepath.Join(s.dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	s.rootCAs = x509.NewCertPool()
	s.Require().True(s.rootCAs.AppendCertsFromPEM(pem), "root.crt must parse as PEM")

	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})
}

// proxyClient returns an http.Client that routes through the suite's proxy
// and trusts the project's own CA - the Go equivalent of curl --proxy
// --cacert.
func (s *ProxySuite) proxyClient() *http.Client {
	return proxyHTTPClient(s.proxyAddr, s.rootCAs)
}

// TestTLSTerminationThroughTheProjectCA covers the curl --proxy --cacert
// case: the proxy MITMs the TLS connection with a certificate signed by the
// project's own CA, and forwards to the nginx container.
func (s *ProxySuite) TestTLSTerminationThroughTheProjectCA() {
	resp := httpGet(s.T(), s.proxyClient(), "https://web.kevin.home/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Contains(string(body), "Welcome to nginx", "must reach the real nginx container")
}

// TestPACFileDirectFetch covers a direct fetch of the PAC file (not through
// the proxy - that would forward-proxy 127.0.0.1 and egress-deny it): it
// sends the environment domain through the proxy and everything else
// DIRECT.
func (s *ProxySuite) TestPACFileDirectFetch() {
	resp := httpGet(s.T(), http.DefaultClient, "http://"+s.proxyAddr+"/proxy.pac")
	defer resp.Body.Close() //nolint:errcheck // read-only response body

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Contains(string(body), "kevin.home", "must send the environment domain through the proxy")
	s.Contains(string(body), `return "DIRECT"`, "must send everything else direct")
}

// TestNoProxyStepReachesUpstreamWithoutProxyEnv covers noproxy: it sets
// proxy: false, so it carries no proxy environment at all, yet still
// reaches web by step name over the docker network.
func (s *ProxySuite) TestNoProxyStepReachesUpstreamWithoutProxyEnv() {
	out := s.waitDockerLogs("kevin-kevin-e2e-proxy-noproxy", "Welcome to nginx", defaultTimeout)
	s.Contains(out, "Welcome to nginx", "noproxy must reach web over the docker network with no proxy env")
}

// TestEgressDefaultDenyReturns403 covers section 3: an unlisted host is
// denied with a 403 naming the host and the CUE fix, and cache-busting
// headers.
func (s *ProxySuite) TestEgressDefaultDenyReturns403() {
	client := s.proxyClient()
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil) //nolint:noctx // one-shot test request
	s.Require().NoError(err)
	req.Header.Set("Accept", "text/plain")

	resp, err := client.Do(req)
	s.Require().NoError(err)
	defer resp.Body.Close() //nolint:errcheck // read-only response body

	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Equal(http.StatusForbidden, resp.StatusCode)
	s.Contains(string(body), "example.com")
	s.Contains(string(body), `proxy: egress: allow: ["example.com"]`)
	s.Contains(resp.Header.Get("Cache-Control"), "no-store")
}

// TestEgressAllowListedHostSucceeds covers proxy: egress: allow: - its own
// project, since it needs a config the shared instance doesn't carry.
func (s *ProxySuite) TestEgressAllowListedHostSucceeds() {
	s.testEgressPolicy(`proxy: egress: allow: ["example.com"]`+"\n", "kevin-e2e-proxy-allow")
}

// TestEgressDenyFalseAllowsEverything covers proxy: egress: deny: false -
// every external host reachable through the proxy with no 403 at all.
func (s *ProxySuite) TestEgressDenyFalseAllowsEverything() {
	s.testEgressPolicy(`proxy: egress: deny: false`+"\n", "kevin-e2e-proxy-nodeny")
}

func (s *ProxySuite) testEgressPolicy(egressCUE, project string) {
	dir := s.T().TempDir()
	src := fmt.Sprintf(`project: %s

`, strconv.Quote(project)) + egressCUE
	s.writeCUE(dir, proxyBlock(s.T())+src)
	s.cleanupProject(project)

	p := s.startKevin(dir, "-C", dir, "run")
	// no step reaches ready in this project - it's proxy-only - so wait for
	// the address lines instead.
	s.waitFor(p, "proxy    http://", defaultTimeout)
	m := addrRE.FindAllStringSubmatch(p.buf.String(), -1)
	var proxyAddr string
	for _, row := range m {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr)

	proxyURL, err := url.Parse("http://" + proxyAddr)
	s.Require().NoError(err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp := httpGet(s.T(), client, "https://example.com/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	s.NotEqual(http.StatusForbidden, resp.StatusCode, "the egress policy must let this host through")

	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT))
	s.waitExit(p, defaultTimeout)
}

// TestRouteWildcardWithoutExternal covers a builtin:route host wildcard
// with no external: true - the proxy's route table wildcard-matches any
// Route.Host with a leading "*.", regardless of external, so "*.web"
// (not "*.web.kevin.home" and not external) must already match
// "anything.web.kevin.home" but not the bare "web.kevin.home". Its own
// project, not the shared webCUE - that constant's own route stays a
// plain, non-wildcard host for the other suites that depend on it.
func (s *ProxySuite) TestRouteWildcardWithoutExternal() {
	s.requireDocker()

	const project = "kevin-e2e-route-wildcard"
	dir := s.T().TempDir()
	s.cleanupProject(project)
	src := fmt.Sprintf(`project: %s

env: {
	web: {uses: "builtin:container", label: "Web", with: {image: "nginx:alpine", expose: web: {port: 80}}}
	web_route: {
		uses:  "builtin:route"
		needs: ["web"]
		with: routes: [{host: "*.web", address: "${needs.web.out.host_80}"}]
	}
}
`, strconv.Quote(project))
	s.writeCUE(dir, proxyBlock(s.T())+src)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("web_route", "ready"), defaultTimeout)
	out := p.buf.String()
	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})

	var proxyAddr string
	for _, row := range addrRE.FindAllStringSubmatch(out, -1) {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr, "output:\n%s", out)

	pem, err := os.ReadFile(filepath.Join(dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	rootCAs := x509.NewCertPool()
	s.Require().True(rootCAs.AppendCertsFromPEM(pem))
	client := proxyHTTPClient(proxyAddr, rootCAs)

	resp := httpGet(s.T(), client, "https://anything.web.kevin.home/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	body, err := io.ReadAll(resp.Body)
	s.Require().NoError(err)
	s.Equal(http.StatusOK, resp.StatusCode)
	s.Contains(string(body), "Welcome to nginx", "a wildcard host must match a subdomain with no external: true")

	resp2 := httpGet(s.T(), client, "https://web.kevin.home/")
	defer resp2.Body.Close() //nolint:errcheck // read-only response body
	s.Equal(http.StatusForbidden, resp2.StatusCode, "a wildcard must not match the bare domain it's registered under")
}
