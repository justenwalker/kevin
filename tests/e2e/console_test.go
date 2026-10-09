//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/suite"
)

// ConsoleSuite covers a user opening the console, the parts checkable
// over plain HTTP: page title and step-label text, live log panels, proxy
// traffic in the traffic view, and a rerun cycling a step back to ready, all
// in one ordered TestConsole walk. A second test checks --open handing the
// console URL to the OS opener (a fake one on PATH). ConsoleBrowserSuite
// below covers the client-side parts in Chrome.
//
// Tier: e2e.
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

	rerunOut, code := s.runToCompletion(dir, "-C", dir, "rerun", "web")
	s.Equal(0, code, "output:\n%s", rerunOut)
	s.Contains(rerunOut, "web ready")
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

// ConsoleBrowserSuite drives the console in a real Chrome (found on this
// machine, skipped when there is none): the parts the HTTP-only
// ConsoleSuite cannot see, namely client-side rendering and the htmx/SSE
// updates. It runs the groups environment, which needs no containers. It
// covers the rerun button and the group sidebar.
//
// Tier: e2e.
type ConsoleBrowserSuite struct {
	e2eSuite

	p       *kevinProc
	console string
	tab     context.Context //nolint:containedctx // a chromedp tab is itself a context, shared by the suite's tests
}

func TestConsoleBrowserSuite(t *testing.T) {
	suite.Run(t, new(ConsoleBrowserSuite))
}

func (s *ConsoleBrowserSuite) SetupSuite() {
	s.requireDocker()

	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+fmt.Sprintf(groupsCUE, "kevin-e2e-console-browser", strconv.Quote(s.echoPluginBin())))
	s.cleanupProject("kevin-e2e-console-browser")
	s.p = s.startKevin(dir, "-C", dir, "run")
	s.waitFor(s.p, stepLine("hold", "up"), defaultTimeout)
	for _, row := range addrRE.FindAllStringSubmatch(s.p.buf.String(), -1) {
		if row[1] == "console" {
			s.console = row[2]
		}
	}
	s.Require().NotEmpty(s.console)

	tab := s.newChromeTab()
	s.tab = tab
	s.Require().NoError(chromedp.Do(tab,
		chromedp.Navigate("http://"+s.console+"/"),
		chromedp.WaitVisible(chromedp.CSS(`#steps`))))
}

// TestGroupCollapsesAndExpands proves the sidebar's group row hides its
// members until clicked, and that the dependency arrows get drawn.
func (s *ConsoleBrowserSuite) TestGroupCollapsesAndExpands() {
	member := `[id="step-db.primary"]`
	toggle := `label[for="group-toggle-db"]`
	s.Require().NoError(chromedp.Do(s.tab,
		chromedp.WaitNotVisible(chromedp.CSS(member)),
		chromedp.Click(chromedp.CSS(toggle)),
		chromedp.WaitVisible(chromedp.CSS(member)),
	))
	s.Require().NoError(chromedp.Do(s.tab, chromedp.WaitReady(chromedp.CSS(`#dep-edges path`))))
	edges, err := chromedp.Run(s.tab, chromedp.Evaluate[int](`document.querySelectorAll('#dep-edges path').length`))
	s.Require().NoError(err)
	s.Require().NoError(chromedp.Do(s.tab,
		chromedp.Click(chromedp.CSS(toggle)),
		chromedp.WaitNotVisible(chromedp.CSS(member)),
	))
	s.Positive(edges, "dependency arrows must be drawn")
}

// TestGroupIsCollapsedAgainAfterReload proves an expanded group is not
// remembered across a page reload.
func (s *ConsoleBrowserSuite) TestGroupIsCollapsedAgainAfterReload() {
	member := `[id="step-db.primary"]`
	s.Require().NoError(chromedp.Do(s.tab,
		chromedp.Click(chromedp.CSS(`label[for="group-toggle-db"]`)),
		chromedp.WaitVisible(chromedp.CSS(member)),
		chromedp.Reload(),
		chromedp.WaitVisible(chromedp.CSS(`#steps`)),
		chromedp.WaitNotVisible(chromedp.CSS(member)),
	))
}

// TestDependencyArrowsEndOnVisibleRows proves every arrow endpoint sits on
// the row of a step that is shown, with the group expanded and collapsed: a
// collapsed group's members are hidden, so no arrow may point at them.
func (s *ConsoleBrowserSuite) TestDependencyArrowsEndOnVisibleRows() {
	const arrows = `(() => {
		const sb = document.getElementById('sidebar').getBoundingClientRect();
		const rows = [...document.querySelectorAll('#steps li')]
			.filter(li => li.getClientRects().length > 0)
			.map(li => { const r = li.getBoundingClientRect(); return r.top - sb.top + r.height / 2; });
		const ends = [...document.querySelectorAll('#dep-edges circle')].map(c => parseFloat(c.getAttribute('cy')));
		return JSON.stringify({
			paths: document.querySelectorAll('#dep-edges path').length,
			circles: ends.length,
			stray: ends.filter(y => !rows.some(r => Math.abs(r - y) < 1)).length,
		});
	})()`
	type result struct {
		Paths   int `json:"paths"`
		Circles int `json:"circles"`
		Stray   int `json:"stray"`
	}
	read := func() result {
		var r result
		raw, err := chromedp.Run(s.tab, chromedp.Evaluate[string](arrows))
		s.Require().NoError(err)
		s.Require().NoError(json.Unmarshal([]byte(raw), &r))
		return r
	}
	toggle := `label[for="group-toggle-db"]`
	member := `[id="step-db.primary"]`

	s.Require().NoError(chromedp.Do(s.tab, chromedp.WaitReady(chromedp.CSS(`#dep-edges path`))))
	collapsed := read()
	s.Positive(collapsed.Paths)
	s.Equal(2*collapsed.Paths, collapsed.Circles, "every arrow has a dot at each end")
	s.Zero(collapsed.Stray, "a collapsed group must not leave arrows on hidden members")

	s.Require().NoError(chromedp.Do(s.tab, chromedp.Click(chromedp.CSS(toggle)), chromedp.WaitVisible(chromedp.CSS(member))))
	s.T().Cleanup(func() { _, _ = chromedp.Run(s.tab, chromedp.Reload()) })
	s.Require().Eventually(func() bool { return read().Paths > collapsed.Paths }, 5*time.Second, 100*time.Millisecond,
		"expanding the group must add the arrow from replica to primary")
	expanded := read()
	s.Greater(expanded.Paths, collapsed.Paths, "expanding the group must add the arrow from replica to primary")
	s.Zero(expanded.Stray, "every arrow must end on a visible row once the group is expanded")

	s.Require().NoError(chromedp.Do(s.tab, chromedp.Click(chromedp.CSS(toggle)), chromedp.WaitNotVisible(chromedp.CSS(member))))
}

// TestStepLogsGrowWithoutAReload proves a rerun's log lines are appended to
// the step's log panel over SSE: the page is never reloaded.
func (s *ConsoleBrowserSuite) TestStepLogsGrowWithoutAReload() {
	lines := func() int {
		n, err := chromedp.Run(s.tab, chromedp.Evaluate[int](`document.getElementById('log-web').children.length`))
		s.Require().NoError(err)
		return n
	}
	s.Require().NoError(chromedp.Do(s.tab, chromedp.WaitReady(chromedp.CSS(`#step-web.state-ready`))))
	_, err := chromedp.Run(s.tab, chromedp.Evaluate[bool](`window.sameDocument = true`))
	s.Require().NoError(err)
	before := lines()

	s.Require().NoError(chromedp.Do(s.tab,
		chromedp.WaitVisible(chromedp.CSS(`#step-web .split-btn`)),
		chromedp.Click(chromedp.CSS(`#step-web .split-btn`)),
	))
	s.Require().Eventually(func() bool { return lines() > before }, defaultTimeout, 100*time.Millisecond,
		"the rerun's log lines must reach the open page")

	same, err := chromedp.Run(s.tab, chromedp.Evaluate[bool](`window.sameDocument === true`))
	s.Require().NoError(err)
	s.True(same, "the page must not have reloaded")
	s.Require().NoError(chromedp.Do(s.tab, chromedp.WaitReady(chromedp.CSS(`#step-web.state-ready`))))
}

// TestRerunButtonCyclesTheStep proves the sidebar's rerun button posts the
// rerun, and the step's row follows it through to ready over SSE with no
// page reload.
func (s *ConsoleBrowserSuite) TestRerunButtonCyclesTheStep() {
	const record = `const cls = () => { const el = document.getElementById('step-web'); return el ? el.className : ''; };
		window.webStates = [cls()];
		new MutationObserver(() => {
			const c = cls();
			if (window.webStates[window.webStates.length - 1] !== c) window.webStates.push(c);
		}).observe(document.getElementById('steps'), {subtree: true, childList: true, attributes: true});
		true`
	_, err := chromedp.Run(s.tab, chromedp.Evaluate[bool](record))
	s.Require().NoError(err)

	s.Require().NoError(chromedp.Do(s.tab,
		chromedp.WaitVisible(chromedp.CSS(`#step-web .split-btn`)),
		chromedp.Click(chromedp.CSS(`#step-web .split-btn`)),
	))
	s.Require().Eventually(func() bool {
		states, err := chromedp.Run(s.tab, chromedp.Evaluate[string](`window.webStates.join('|')`))
		if err != nil {
			return false
		}
		seen := strings.Split(states, "|")
		left := slices.ContainsFunc(seen, func(c string) bool { return !strings.Contains(c, "state-ready") })
		return left && strings.Contains(seen[len(seen)-1], "state-ready")
	}, defaultTimeout, 100*time.Millisecond, "the row must leave ready and return to it, over SSE")
}

// TestOpenFlagLaunchesTheBrowserOnTheConsole proves "run --open" starts the
// OS opener on the console's own address. A fake opener on PATH records its
// argument, so no real browser launches.
func (s *ConsoleSuite) TestOpenFlagLaunchesTheBrowserOnTheConsole() {
	shim := s.T().TempDir()
	recorded := filepath.Join(shim, "opened.txt")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + strconv.Quote(recorded) + "\n"
	for _, name := range []string{"open", "xdg-open"} {
		s.Require().NoError(os.WriteFile(filepath.Join(shim, name), []byte(script), 0o755))
	}

	dir := s.T().TempDir()
	s.writeCUE(dir, proxyBlock(s.T())+fmt.Sprintf(groupsCUE, "kevin-e2e-console-open", strconv.Quote(s.echoPluginBin())))
	s.cleanupProject("kevin-e2e-console-open")
	p := s.startKevinWithEnv(dir, []string{"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH")},
		"-C", dir, "run", "--open")
	s.waitFor(p, stepLine("hold", "up"), defaultTimeout)

	var consoleAddr string
	for _, row := range addrRE.FindAllStringSubmatch(p.buf.String(), -1) {
		if row[1] == "console" {
			consoleAddr = row[2]
		}
	}
	s.Require().NotEmpty(consoleAddr)
	s.Require().Eventually(func() bool {
		got, err := os.ReadFile(recorded)
		return err == nil && strings.TrimSuffix(string(got), "/") == "http://"+consoleAddr
	}, 10*time.Second, 100*time.Millisecond, "the opener must be given the console address %q", consoleAddr)
}
