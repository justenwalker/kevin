//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

// ConsoleSuite covers docs/MANUAL_TESTING.md section 5, the parts checkable
// over plain HTTP: page title and step-label text, live log panels, proxy
// traffic showing up in the traffic view, and a rerun cycling a step back
// to ready. Left manual: --open's actual browser launch, and pointing a
// real browser at the PAC URL. SetupSuite brings up one project; a single
// TestConsole method walks the sequence in order, since it's inherently
// ordered rather than independently schedulable facts.
type ConsoleSuite struct {
	e2eSuite
}

func TestConsoleSuite(t *testing.T) {
	suite.Run(t, new(ConsoleSuite))
}

func (s *ConsoleSuite) TestConsole() {
	s.requireDocker()

	const project = "kevin-e2e-console"
	dir := s.project(project, webCUE)

	p := s.startKevin(dir, "-C", dir, "run")
	s.waitFor(p, stepLine("noproxy", "ready"), defaultTimeout)
	out := p.buf.String()

	var consoleAddr, proxyAddr string
	for _, row := range addrRE.FindAllStringSubmatch(out, -1) {
		switch row[1] {
		case "console":
			consoleAddr = row[2]
		case "proxy":
			proxyAddr = row[2]
		}
	}
	s.Require().NotEmpty(consoleAddr, "output:\n%s", out)
	s.Require().NotEmpty(proxyAddr, "output:\n%s", out)

	s.T().Cleanup(func() {
		require := s.Require()
		require.NoError(p.cmd.Process.Signal(syscall.SIGINT))
		s.waitExit(p, defaultTimeout)
	})

	body := s.getPage(consoleAddr)
	s.Contains(body, "<title>kevin: "+project+"</title>")
	s.Contains(body, "Web Server", "step cards must show the label, not the bare step name")
	s.Contains(body, "starting nginx:alpine", "the step's own log lines must be visible")

	s.generateProxyTraffic(dir, proxyAddr)
	body = s.getPage(consoleAddr)
	s.Contains(body, "<td>web.kevin.home</td>", "proxy traffic must show up in the traffic view")

	upCountBefore := strings.Count(out, stepLine("web", "up"))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+consoleAddr+"/steps/web/rerun", strings.NewReader("cascade=false"))
	s.Require().NoError(err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	_ = resp.Body.Close()
	s.Equal(http.StatusAccepted, resp.StatusCode)

	s.Require().Eventually(func() bool {
		return strings.Count(p.buf.String(), stepLine("web", "up")) > upCountBefore
	}, defaultTimeout, 100*time.Millisecond, "rerun must cycle the step back through up")

	s.Require().Eventually(func() bool {
		return strings.Count(p.buf.String(), stepLine("web", "ready")) >= 2
	}, defaultTimeout, 100*time.Millisecond, "rerun must reach ready again")
}

func (s *ConsoleSuite) getPage(consoleAddr string) string {
	resp := httpGet(s.T(), http.DefaultClient, "http://"+consoleAddr+"/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	body := readAll(s.T(), resp.Body)
	s.Equal(http.StatusOK, resp.StatusCode)
	return body
}

// generateProxyTraffic sends one request through the proxy so it shows up
// in the console's traffic view.
func (s *ConsoleSuite) generateProxyTraffic(dir, proxyAddr string) {
	pem, err := os.ReadFile(filepath.Join(dir, ".kevin", "root.crt"))
	s.Require().NoError(err)
	client := proxyHTTPClient(proxyAddr, newCertPool(pem))

	resp := httpGet(s.T(), client, "https://web.kevin.home/")
	defer resp.Body.Close() //nolint:errcheck // read-only response body
	s.Equal(http.StatusOK, resp.StatusCode, "traffic generation request must itself succeed")
}
