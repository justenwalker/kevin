//go:build e2e

// Package e2e drives the compiled kevin binary as a subprocess, black-box,
// against the parts of docs/MANUAL_TESTING.md that need no GUI, browser,
// minisign, or external OCI registry. It imports no kevin package.
package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

// defaultTimeout bounds every subprocess run. A hang fails the test loudly,
// with whatever output was captured, instead of blocking forever - this is
// the guard for the kind of regression that let "kevin run --keep" return
// without ever blocking for Ctrl-C.
const defaultTimeout = 60 * time.Second

var (
	kevinBinOnce      = sync.OnceValues(buildKevin)
	echoPluginBinOnce = sync.OnceValues(buildEchoPlugin)
)

// repoRoot locates the repository root from this file's own path, so the
// go build subprocesses below work regardless of the test binary's working
// directory.
func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("e2e: cannot locate the repository root")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func buildKevin() (string, error) {
	return buildBinary("kevin", "github.com/justenwalker/kevin/cmd/kevin")
}

func buildEchoPlugin() (string, error) {
	return buildBinary("kevin-plugin-echo", "github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
}

// buildBinary builds pkg into a fresh temp dir, the same way
// buildEchoPluginForIntegration does in internal/engine/integration_test.go.
// Building here rather than shelling out to "gnob build" keeps "go test
// -tags e2e ./tests/e2e/..." self-contained.
func buildBinary(name, pkg string) (string, error) {
	dir, err := os.MkdirTemp("", "kevin-e2e-"+name)
	if err != nil {
		return "", fmt.Errorf("e2e: mkdir temp: %w", err)
	}
	bin := filepath.Join(dir, name)
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin, pkg)
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("e2e: build %s: %w: %s", pkg, err, out)
	}
	return bin, nil
}

// syncBuffer is a concurrency-safe growable buffer: the subprocess writes to
// it from its own goroutine while the test polls it from another.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Contains(b.buf.String(), s)
}

// stepLine builds the exact text engine.emit writes for a step transition
// ("%-16s %s\n" in internal/engine/engine.go), so tests can wait for or
// search the plain event stream without guessing at padding.
func stepLine(step, status string) string {
	return fmt.Sprintf("%-16s %s", step, status)
}

// kevinProc is a running kevin subprocess: its combined output and its exit,
// observable any number of times without consuming either.
type kevinProc struct {
	cmd    *exec.Cmd
	buf    *syncBuffer
	waitCh chan struct{} // closed once cmd.Wait returns
	err    error         // valid once waitCh is closed
}

// e2eSuite is the base every section's suite embeds, for the harness
// helpers shared across all of them.
type e2eSuite struct {
	suite.Suite
}

// kevinBin returns the path to the kevin binary, built once for the whole
// run.
func (s *e2eSuite) kevinBin() string {
	bin, err := kevinBinOnce()
	s.Require().NoError(err)
	return bin
}

// echoPluginBin returns the path to the kevin-plugin-echo binary, built
// once for the whole run.
func (s *e2eSuite) echoPluginBin() string {
	bin, err := echoPluginBinOnce()
	s.Require().NoError(err)
	return bin
}

// requireDocker skips the test when no docker daemon answers. It shells
// directly to docker rather than importing internal/docker, since this
// package stays black-box.
func (s *e2eSuite) requireDocker() {
	s.T().Helper()
	if out, err := exec.CommandContext(context.Background(), "docker", "info").CombinedOutput(); err != nil {
		s.T().Skip("docker is not available:", err, string(out))
	}
}

// project writes cueSrc, with project spliced in as its %s verb, to a fresh
// temp dir's kevin.cue and returns the dir. It also registers cleanup of
// any docker resources the project may leave behind.
func (s *e2eSuite) project(project, cueSrc string) string {
	dir := s.T().TempDir()
	s.writeCUE(dir, fmt.Sprintf(cueSrc, project))
	s.cleanupProject(project)
	return dir
}

// writeCUE writes src to dir/kevin.cue. Callers that need more than one
// substitution in their template (a project name and a built plugin path,
// say) render src themselves and call this directly instead of [project].
func (s *e2eSuite) writeCUE(dir, src string) {
	s.writeCUEFile(dir, "kevin.cue", src)
}

// writeCUEFile writes src to dir/name - for a named environment file
// (staging.kevin.cue) or a second format alongside the default kevin.cue.
func (s *e2eSuite) writeCUEFile(dir, name, src string) {
	t := s.T()
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600))
}

// cleanupProject force-removes any containers and the network left behind
// under kevin.project=project. It's belt-and-braces for a test that fails
// before reaching its own interrupt step - the happy path relies on kevin's
// own teardown, since that's exactly what's under test.
func (s *e2eSuite) cleanupProject(project string) {
	s.T().Cleanup(func() {
		// A fresh, uncanceled context: t's own is already done by the time
		// Cleanup funcs run, and this cleanup must still reach docker.
		ctx := context.Background()
		for _, id := range s.containerIDsForProject(project) {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
		_ = exec.CommandContext(ctx, "docker", "network", "rm", "kevin-"+project).Run()
	})
}

// containerIDsForProject lists every container (running or not) labeled
// kevin.project=project.
func (s *e2eSuite) containerIDsForProject(project string) []string {
	out, err := exec.CommandContext(context.Background(), "docker", "ps", "-aq", "--filter", "label=kevin.project="+project).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}

// startKevin starts the kevin binary against dir with args, streaming
// combined stdout+stderr into the process's buffer.
func (s *e2eSuite) startKevin(dir string, args ...string) *kevinProc {
	return s.startKevinWithEnv(dir, nil, args...)
}

// startKevinWithEnv is [e2eSuite.startKevin] with extraEnv appended to the
// subprocess's environment - for KEVIN_ENV or an env var a CEL expression
// reads.
func (s *e2eSuite) startKevinWithEnv(dir string, extraEnv []string, args ...string) *kevinProc {
	t := s.T()
	t.Helper()

	buf := &syncBuffer{}
	cmd := exec.CommandContext(context.Background(), s.kevinBin(), args...)
	cmd.Dir = dir
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Env = append(append(os.Environ(), "NO_COLOR=1"), extraEnv...)
	require.NoError(t, cmd.Start(), "start kevin")

	p := &kevinProc{cmd: cmd, buf: buf, waitCh: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.waitCh)
	}()
	return p
}

// running reports whether p's process has not yet exited.
func (s *e2eSuite) running(p *kevinProc) bool {
	select {
	case <-p.waitCh:
		return false
	default:
		return true
	}
}

// waitFor blocks until p's output contains until, or timeout elapses -
// failing the test on the timeout rather than hanging.
func (s *e2eSuite) waitFor(p *kevinProc, until string, timeout time.Duration) {
	t := s.T()
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p.buf.Contains(until) {
			return
		}
		if !s.running(p) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !p.buf.Contains(until) {
		t.Fatalf("timed out waiting for %q, output:\n%s", until, p.buf.String())
	}
}

// waitExit blocks on p's exit, failing the test on a hard timeout rather
// than hanging forever - the guard against exactly the kind of hang the
// "kevin run --keep" regression produced.
func (s *e2eSuite) waitExit(p *kevinProc, timeout time.Duration) int {
	t := s.T()
	t.Helper()

	select {
	case <-p.waitCh:
		return exitCodeOf(p.err)
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-p.waitCh
		t.Fatalf("kevin did not exit within %s, output:\n%s", timeout, p.buf.String())
		return -1
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// runUntil starts kevin against dir with args, waits for until to appear in
// its combined output, sends SIGINT, and waits for exit.
func (s *e2eSuite) runUntil(dir, until string, args ...string) (string, int) {
	p := s.startKevin(dir, args...)
	s.waitFor(p, until, defaultTimeout)
	s.Require().NoError(p.cmd.Process.Signal(syscall.SIGINT), "send SIGINT")
	code := s.waitExit(p, defaultTimeout)
	return p.buf.String(), code
}

// runToCompletion starts kevin against dir with args and waits for it to
// exit on its own - for one-shot commands (validate, init, plugin list,
// setup) that don't need an interrupt.
func (s *e2eSuite) runToCompletion(dir string, args ...string) (string, int) {
	p := s.startKevin(dir, args...)
	code := s.waitExit(p, defaultTimeout)
	return p.buf.String(), code
}

// dockerLogs returns the combined stdout+stderr of a container by name.
func dockerLogs(name string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "docker", "logs", name).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("e2e: docker logs %s: %w: %s", name, err, out)
	}
	return string(out), nil
}

// waitDockerLogs polls a container's combined logs until they contain want,
// or timeout elapses - a container's own command (e.g. a probe's wget) may
// still be running just after kevin reports the step ready.
func (s *e2eSuite) waitDockerLogs(name, want string, timeout time.Duration) string {
	t := s.T()
	t.Helper()

	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := dockerLogs(name)
		if err == nil {
			last = out
			if strings.Contains(out, want) {
				return out
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in docker logs %s, got:\n%s", want, name, last)
	return ""
}

// sigkill sends SIGKILL to p and waits for it to die - for the
// crash-resilience test, a real crash rather than a clean Ctrl-C.
func (s *e2eSuite) sigkill(p *kevinProc) {
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	s.waitExit(p, defaultTimeout)
}

// newCertPool parses pem (a project's root.crt) into a cert pool.
func newCertPool(pem []byte) *x509.CertPool {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		panic("e2e: root.crt did not parse as PEM")
	}
	return pool
}

// proxyHTTPClient is the Go equivalent of curl --proxy --cacert: it routes
// every request through proxyAddr and trusts only pool.
func proxyHTTPClient(proxyAddr string, pool *x509.CertPool) *http.Client {
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		panic(err)
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: pool},
	}}
}

// httpGet issues a GET through client with a real context, so callers stay
// noctx-clean without doing this boilerplate themselves.
func httpGet(t *testing.T, client *http.Client, target string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// readAll reads r fully, failing the test on error.
func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	body, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(body)
}
