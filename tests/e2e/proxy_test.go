//go:build e2e

package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/chromedp/chromedp"
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

// ProxySuite covers a user going through the proxy: TLS termination,
// routing, NO_PROXY, and egress control. SetupSuite brings up one
// web-style project once, shared read-only across the TLS/PAC/deny tests;
// the egress-allow and egress-deny-false variants each need their own
// config, so they run their own project per test.
//
// Tier: e2e.
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

// TestPACFileRoutesChrome points a real Chrome at the PAC URL. The
// environment's hostname must load through the proxy, and an unrelated
// hostname must go direct, which the local server seeing the request
// proves: through the proxy it would have been egress-denied. It runs its
// own project with a throwaway state dir, so kevin generates a root the
// machine has never trusted and Chrome can only accept it through the
// public-key hash passed on its command line.
func (s *ProxySuite) TestPACFileRoutesChrome() {
	s.newChromeTab() // skips before the bring-up below when there is no Chrome
	var directHits atomic.Int64
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		_, _ = io.WriteString(w, "reached directly")
	}))
	defer direct.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(direct.URL, "http://"))
	s.Require().NoError(err)

	home := s.T().TempDir()
	dir := s.project("kevin-e2e-proxy-pac", webCUE)
	p := s.startKevinWithEnv(dir, []string{"HOME=" + home, "KEVIN_USER_STATE_DIR=" + filepath.Join(home, ".kevin")},
		"-C", dir, "run")
	s.waitFor(p, stepLine("web_route", "ready"), defaultTimeout)
	var proxyAddr string
	for _, row := range addrRE.FindAllStringSubmatch(p.buf.String(), -1) {
		if row[1] == "proxy" {
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(proxyAddr)
	pemData, err := os.ReadFile(filepath.Join(dir, ".kevin", "root.crt"))
	s.Require().NoError(err)

	open := func(hash string) context.Context {
		return s.newChromeTab(
			chromedp.Flag("proxy-pac-url", "http://"+proxyAddr+"/proxy.pac"),
			chromedp.Flag("ignore-certificate-errors-spki-list", hash),
			chromedp.Flag("host-resolver-rules", "MAP direct.example.test 127.0.0.1"),
		)
	}

	untrusted := open(base64.StdEncoding.EncodeToString(make([]byte, sha256.Size)))
	err = chromedp.Do(untrusted, chromedp.Navigate("https://web.kevin.home/"))
	s.Require().ErrorContains(err, "ERR_CERT_", "without the right key hash Chrome must reject the kevin certificate")

	tab := open(spkiHash(s.T(), pemData))
	s.Require().NoError(chromedp.Do(tab, chromedp.Navigate("https://web.kevin.home/")))
	page, err := chromedp.Run(tab, chromedp.Text(chromedp.CSS("body")))
	s.Require().NoError(err)
	s.Contains(page, "Welcome to nginx", "the environment hostname must load through the proxy")

	s.Require().NoError(chromedp.Do(tab, chromedp.Navigate("http://direct.example.test:"+port+"/")))
	page, err = chromedp.Run(tab, chromedp.Text(chromedp.CSS("body")))
	s.Require().NoError(err)
	s.Contains(page, "reached directly")
	s.Positive(directHits.Load(), "an unrelated host must bypass the proxy")
}

// TestNoProxyStepReachesUpstreamWithoutProxyEnv covers noproxy: it sets
// proxy: false, so it carries no proxy environment at all, yet still
// reaches web by step name over the docker network.
func (s *ProxySuite) TestNoProxyStepReachesUpstreamWithoutProxyEnv() {
	out := s.waitDockerLogs(s.stepContainer("kevin-e2e-proxy", "noproxy"), "Welcome to nginx", defaultTimeout)
	s.Contains(out, "Welcome to nginx", "noproxy must reach web over the docker network with no proxy env")
}

// TestEgressDefaultDenyReturns403 covers default-deny egress: an unlisted host is
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
