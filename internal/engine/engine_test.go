package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/relay/relaytest"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/protos/pb"
)

// echoPlugin is the path of the compiled echo plugin. Every test uses it.
var echoPlugin = sync.OnceValues(buildEchoPlugin)

// dockerClient inspects docker resources directly in tests, regardless of
// which engine the code path under test resolved - every test here runs
// against the default (docker) engine.
var dockerClient = docker.Client{}

func buildEchoPlugin() (string, error) {
	dir, err := os.MkdirTemp("", "kevin-plugin-*")
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "kevin-plugin-echo")
	cmd := exec.CommandContext(context.Background(), "go", "build", "-o", bin,
		"github.com/justenwalker/kevin/cmd/kevin-plugin-echo")
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("build echo plugin: %w: %s", buildErr, out)
	}
	return bin, nil
}

// packagedEchoPlugin is the path of a file: source tar built around the
// compiled echo plugin. TestRun's "starts a file-source plugin" case uses it.
var packagedEchoPlugin = sync.OnceValues(buildPackagedEchoPlugin)

func buildPackagedEchoPlugin() (string, error) {
	bin, err := echoPlugin()
	if err != nil {
		return "", err
	}
	binData, err := os.ReadFile(bin)
	if err != nil {
		return "", err
	}

	manifest, err := json.Marshal(pluginpkg.Manifest{
		ManifestVersion: pluginpkg.CurrentManifestVersion,
		Name:            "echo",
		Version:         "1.0.0",
		Entrypoint:      "kevin-plugin-echo",
	})
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "kevin-plugin-pkg-*")
	if err != nil {
		return "", err
	}
	pkgPath := filepath.Join(dir, "echo.tar")
	f, err := os.Create(pkgPath)
	if err != nil {
		return "", err
	}

	tw := tar.NewWriter(f)
	for _, entry := range []struct {
		name string
		mode int64
		data []byte
	}{
		{name: pluginpkg.ManifestFile, mode: 0o644, data: manifest},
		{name: "kevin-plugin-echo", mode: 0o755, data: binData},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name: entry.name, Typeflag: tar.TypeReg, Mode: entry.mode, Size: int64(len(entry.data)),
		}); err != nil {
			return "", err
		}
		if _, err := tw.Write(entry.data); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return pkgPath, nil
}

// project writes a kevin.cue into a temporary directory, and returns the
// directory. proxy.listen, proxy.gateway_port, and console.listen are all
// required fields with no default, so project fills them in with freshly
// reserved ports - a case that needs to control them itself uses configDir
// instead, with proxyBlock to fill in the same required fields.
// project writes a kevin.cue fixture under a fresh t.TempDir and returns
// its directory. Unless body sets its own project field (a handful of
// tests need one fixed across a t.Cleanup or a second process), it gets a
// name unique to this call: project defaults to the directory's own base
// name otherwise (config.projectName), which is just t.TempDir's per-call
// sequence number ("001", "002", ...) - identical across nearly every
// subtest in this file, so every one of them would create, use, and tear
// down a Docker network of the exact same name back to back. Real usage
// never recreates a network under the same name in rapid succession;
// hammering that pattern hundreds of times a test run is what surfaces
// Docker's own daemon-side removal/recreate race, not a bug in the code
// under test.
func project(t *testing.T, body string) string {
	t.Helper()
	bin, err := echoPlugin()
	require.NoError(t, err)

	dir := t.TempDir()
	src := "plugins: echo: cmd: " + strconv.Quote(bin) + "\n" + proxyBlock(t)
	if !strings.Contains(body, "project:") {
		name := config.SlugName(t.Name()) + "-" + filepath.Base(dir)
		src += "project: " + strconv.Quote(name) + "\n"
		removeProject(t, name)
	} else if m := projectField.FindStringSubmatch(body); m != nil {
		removeProject(t, m[1])
	}
	src += body
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
}

// projectField finds the name of a quoted project field in a fixture body.
var projectField = regexp.MustCompile(`(?m)^\s*project:\s*"([^"]+)"`)

// removeProject removes, when t ends, the containers and the network that a
// run with Keep leaves behind for the project called name. A failure is
// ignored: the run may have removed them already.
func removeProject(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		containers, _ := dockerClient.ListByLabel(ctx, cri.LabelProject, name)
		for _, container := range containers {
			_ = dockerClient.Remove(ctx, container)
		}
		_ = dockerClient.NetworkRemove(ctx, NetworkName(name))
	})
}

// proxyBlock is a "proxy: {listen: ..., gateway_port: ..., egress: deny:
// ...}\nconsole: listen: ...\n" CUE snippet naming freshly reserved, free
// ports and a deny default, not concrete values - proxy.listen,
// proxy.gateway_port, console.listen, and proxy.egress.deny all carry no
// schema default, so every fixture that reaches config.Config() needs
// these, but a body that sets its own value for any of the four must
// still be able to unify its concrete value over this block's default
// instead of conflicting with it.
func proxyBlock(t *testing.T) string {
	t.Helper()
	_, gatewayPort, err := net.SplitHostPort(freeAddr(t))
	require.NoError(t, err)
	return "proxy: {listen: string | *" + strconv.Quote(freeAddr(t)) + ", gateway_port: int | *" + gatewayPort + ", egress: deny: bool | *true}\n" +
		"console: listen: string | *" + strconv.Quote(freeAddr(t)) + "\n"
}

// watcher collects the engine output. When the environment is up, watcher
// cancels the run. Thus a test needs no timer.
type watcher struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	until  string
	cancel context.CancelFunc
}

func (w *watcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if w.until != "" && strings.Contains(w.buf.String(), w.until) {
		w.until = ""
		w.cancel()
	}
	return n, err
}

func (w *watcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// getVia issues a GET request to rawURL through client.
func getVia(t *testing.T, client *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	return client.Do(req)
}

// freeAddr finds a free TCP port and returns its address. A race remains
// between the close and the caller's bind, but the risk is small enough for a
// test.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// waitForCount polls until w's buffer contains at least n occurrences of
// substr, or timeout elapses. A poll, not watcher's until/cancel pair,
// because a rerun test waits for a line it has already seen once before.
func waitForCount(t *testing.T, w *watcher, substr string, n int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if strings.Count(w.String(), substr) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %dx %q; got:\n%s", n, substr, w.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// getPage fetches the console's page over base.
func getPage(t *testing.T, client *http.Client, base string) string {
	t.Helper()
	resp, err := getVia(t, client, base+"/")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// postRerun calls the console's rerun endpoint for step, the same request the
// sidebar's Rerun buttons send.
func postRerun(t *testing.T, client *http.Client, base, step string, cascade bool) {
	t.Helper()
	form := url.Values{"cascade": {strconv.FormatBool(cascade)}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		base+"/steps/"+step+"/rerun", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// configDir writes src as a kevin.cue in a fresh temporary directory, and
// returns the directory. Unlike project, it adds nothing of its own - a case
// that must control every field of the config (a nonexistent plugin binary,
// a reserved namespace) uses this instead.
func configDir(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
}

// runUntil runs dir's environment in the env scope, canceling once the
// output contains until, and returns what the run produced.
func runUntil(t *testing.T, dir, until string) (*watcher, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{until: until, cancel: cancel}
	err := Run(ctx, Options{Dir: dir, Scope: config.ScopeEnv, Events: w})
	return w, err
}

// runKeep runs dir's environment under scope with Keep set, canceling once
// until appears in the output. Keep only skips teardown - Run still waits
// for cancellation like a normal run.
func runKeep(t *testing.T, dir, scope, until string) (*watcher, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{until: until, cancel: cancel}
	err := Run(ctx, Options{Dir: dir, Scope: scope, Keep: true, Events: w})
	return w, err
}

// runEnv runs dir's environment in the env scope with no events sink, for a
// case that only checks the returned error.
func runEnv(t *testing.T, dir string) error {
	t.Helper()
	return Run(t.Context(), Options{Dir: dir, Scope: config.ScopeEnv})
}

// runAsync starts dir's environment in the background under ctx, and returns
// the channel Run's error arrives on, for a case that must interact with the
// environment while it is still up.
func runAsync(t *testing.T, ctx context.Context, dir string, w *watcher) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Dir: dir, Scope: config.ScopeEnv, Events: w})
	}()
	return done
}

// TestMain points relay.Ref at relaytest.Tag for every test in this
// package: Run always starts a relay, and internal/version.VERSION in this
// checkout still names the last real release, so the unoverridden default
// would resolve to that released image instead of the one a test builds
// from source with relaytest.UseDevImage - silently talking to a relay that
// predates whatever relay-side change is still unreleased on this branch.
// Setting this here, once, covers every test that calls Run() even
// indirectly, not just the ones that remember to call requireRelay first.
func TestMain(m *testing.M) {
	_ = os.Setenv("KEVIN_RELAY_IMAGE", relaytest.Tag)
	os.Exit(m.Run())
}

// requireRelay skips a test when Docker does not answer, then builds the
// relay image from this checkout.
func requireRelay(t *testing.T) {
	t.Helper()
	requireDocker(t)
	relaytest.UseDevImage(t)
}

func TestRun(t *testing.T) {
	t.Run("brings up and tears down in dependency order", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	a: {uses: "echo:echo", with: {message: "A", outputs: greeting: "hi"}}
	b: {uses: "echo:echo", needs: ["a"], with: message: "B"}
	c: {uses: "echo:echo", needs: ["a"], with: message: "C"}
	d: {uses: "echo:echo", needs: ["b", "c"], with: message: "D"}
}
`)
		w, err := runUntil(t, dir, "d                ready")
		require.NoError(t, err)

		out := w.String()

		// Every step came up, and the engine removed every step again.
		for _, step := range []string{"a", "b", "c", "d"} {
			assert.Contains(t, out, step+"                ready", "step %s must come up", step)
			assert.Contains(t, out, step+"                removed", "the engine must remove step %s", step)
		}

		// The outputs reached the dependent step. A step's own log lines go
		// to the console and the durable log file, not the terminal.
		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "saw a: map[greeting:hi step:a]")

		// Step d came up last, and the engine removed it first.
		assert.Less(t, strings.Index(out, "a                ready"), strings.Index(out, "d                ready"))
		assert.Less(t, strings.Index(out, "d                removed"), strings.Index(out, "a                removed"))
	})

	// "skips down for a step with no downer" proves that a step type reporting
	// no Downer via Info never gets its Down RPC called during teardown.
	// run.down emits "down" right before the RPC, and skips both the RPC and
	// that line together for a step with no Downer - echo:probe implements no
	// Down method, so if the skip didn't happen the plugin would return
	// Unimplemented and Run would return a non-nil error.
	t.Run("skips down for a step with no downer", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	p: {uses: "echo:probe"}
}
`)
		w, err := runUntil(t, dir, "p                ready")
		require.NoError(t, err)

		out := w.String()
		assert.Contains(t, out, "p                ready", "the probe step must come up")
		assert.NotContains(t, out, "p                down", "a step type with no Downer must never have Down called")
	})

	// "forwards plugin-declared details to the card" proves a step's
	// with-block "details" reach the console: run.up emits "detail: <label>"
	// right before calling AddStepDetail, for every entry in the Up result's
	// Details field.
	t.Run("forwards plugin-declared details to the card", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	a: {uses: "echo:echo", with: details: [{label: "admin password", value: "hunter2", copyable: true}]}
}
`)
		w, err := runUntil(t, dir, "a                ready")
		require.NoError(t, err)

		assert.Contains(t, w.String(), "a                detail: admin password")
	})

	t.Run("starts a file-source plugin", func(t *testing.T) {
		requireRelay(t)
		pkgPath, err := packagedEchoPlugin()
		require.NoError(t, err)

		dir := configDir(t, "plugins: echo: file: "+strconv.Quote(pkgPath)+"\n"+proxyBlock(t)+
			`env: a: {uses: "echo:echo", with: message: "A"}`+"\n")

		w, err := runUntil(t, dir, "a                ready")
		require.NoError(t, err)

		out := w.String()
		assert.Contains(t, out, "a                ready", "the extracted plugin must come up like any other")
		assert.Contains(t, out, "a                removed")
	})

	t.Run("renders progress and carries the environment", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: a: {uses: "echo:echo", with: {message: "A", delay: "10ms"}}
`)
		w, err := runUntil(t, dir, "a                ready")
		require.NoError(t, err)

		assert.Contains(t, w.String(), "a                waiting", "a progress event must reach the output")

		// The engine creates the workspace before it runs any step.
		assert.DirExists(t, filepath.Join(dir, WorkspaceDir))

		// The authority must exist before a step runs, because a step receives
		// the certificate in its environment.
		assert.FileExists(t, filepath.Join(dir, WorkspaceDir, ca.CertFile))
	})

	t.Run("uses the setup scope separately", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: trust: {uses: "echo:echo", with: message: "installing"}
env:   api:   {uses: "echo:echo", with: message: "serving"}
`)
		w, err := runKeep(t, dir, config.ScopeSetup, fmt.Sprintf("%-16s %s", "trust", "ready"))
		require.NoError(t, err)

		out := w.String()
		assert.NotContains(t, out, "removed", "Keep must leave the steps in place")

		// A step's own log lines no longer reach the terminal (w) - they go
		// to the console and the durable log file.
		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "installing", "the setup scope must run")
		assert.NotContains(t, string(logs), "serving", "the env scope must not run")
	})

	// An empty scope has no step to wait on, but Keep and NoWait together -
	// exactly what setup uses - must still let Run return with none.
	t.Run("accepts an empty scope", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `env: {}`)
		err := Run(t.Context(), Options{Dir: dir, Scope: config.ScopeEnv, Keep: true, NoWait: true})
		require.NoError(t, err)
	})

	// The MCP server is mounted onto the console's own listener rather than
	// binding one of its own - this proves it actually answers a real MCP
	// tool call there.
	t.Run("serves the mcp server alongside the console", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `env: {}`)

		var callErr error
		var result *mcp.CallToolResult
		err := Run(t.Context(), Options{
			Dir: dir, Scope: config.ScopeEnv, Keep: true, NoWait: true,
			OnEnvironment: func(env *pb.Environment) {
				client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
				sess, connErr := client.Connect(t.Context(), &mcp.StreamableClientTransport{
					Endpoint: "http://" + env.GetConsoleAddr() + mcpserver.Path,
				}, nil)
				if connErr != nil {
					callErr = connErr
					return
				}
				defer func() { _ = sess.Close() }()

				result, callErr = sess.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_steps"})
			},
		})
		require.NoError(t, err)

		require.NoError(t, callErr)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
	})

	// setup relies on NoWait to bring its steps up and return without ever
	// waiting for ctx - this proves that mechanism directly, with a ctx that
	// is never canceled: if NoWait stopped skipping the wait, this would
	// hang until the test times out. NoWait alone, with Keep unset, must
	// still leave the step in place.
	t.Run("returns immediately with NoWait", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: a: {uses: "echo:echo", with: message: "A"}
`)
		w := &watcher{}
		err := Run(t.Context(), Options{Dir: dir, Scope: config.ScopeEnv, NoWait: true, Events: w})
		require.NoError(t, err)

		assert.NotContains(t, w.String(), "removed", "NoWait implies Keep - it must leave the step in place")
	})

	// "tears down what came up when a step fails" proves a failed step no
	// longer ends the session on its own: Run only tears down once the
	// caller cancels ctx (here, once the test has seen boom's failure land),
	// and it still removes what came up and never lets a skipped dependent
	// run.
	t.Run("tears down what came up when a step fails", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	a:    {uses: "echo:echo", with: message: "A"}
	boom: {uses: "echo:echo", needs: ["a"], with: fail: true}
	next: {uses: "echo:echo", needs: ["boom"], with: message: "never"}
}
`)

		w, err := runUntil(t, dir, "boom             failed:")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failure requested by configuration")

		out := w.String()
		assert.Contains(t, out, "a                removed", "the engine must remove the step that came up")
		assert.NotContains(t, out, "next             up", "a step after the failure must not run")
		assert.NotContains(t, out, "boom             removed", "a step that failed on its own is left to its plugin")
	})

	t.Run("delivers the plugin config before any step runs", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
plugins: echo: config: greeting: "hello"
env: a: {uses: "echo:echo", with: message: "A"}
`)

		_, err := runUntil(t, dir, "a                ready")
		require.NoError(t, err)

		// A step's own log lines no longer reach the terminal (w) - they go
		// to the console and the durable log file. Read the file to confirm
		// the config still reached the step before it ran.
		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "provider greeting: hello",
			"the engine must deliver the plugin config before the step runs")
	})

	t.Run("routes two step types of one plugin to one process", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
env: {
	ok:   {uses: "echo:echo", with: message: "OK"}
	boom: {uses: "echo:fail", needs: ["ok"]}
}
`)
		w, err := runUntil(t, dir, "boom             failed:")
		require.Error(t, err, "the fail step type must fail Up")
		assert.Contains(t, err.Error(), "the fail step always fails, on purpose",
			"the step's human-facing message must reach the terminal instead of its raw error chain")

		out := w.String()
		assert.Contains(t, out, fmt.Sprintf("%-16s %s", "ok", "ready"),
			"the echo step type must come up in the same run as the fail step type")
	})
}

// TestRunRemovesAStepThatWasInUpAtCancellation proves a step that is still in
// Up when the run is canceled gets a Down with no outputs, because it may have
// made resources that Up never reported.
func TestRunRemovesAStepThatWasInUpAtCancellation(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a:    {uses: "echo:echo", with: message: "A"}
	slow: {uses: "echo:echo", needs: ["a"], with: {message: "S", delay: "1h"}}
}
`)

	w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "slow", "waiting"))
	require.NoError(t, err)

	out := w.String()
	assert.Contains(t, out, "slow             removed", "the interrupted step must get a Down")
	assert.Contains(t, out, "a                removed")
	logs, readErr := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
	require.NoError(t, readErr)
	assert.Contains(t, string(logs), "removing slow")
}

func TestStepTimeout(t *testing.T) {
	requireRelay(t)

	t.Run("fails a step whose Up outlives it and removes it at shutdown", func(t *testing.T) {
		dir := project(t, `
env: {
	slow: {uses: "echo:echo", timeout: "50ms", with: {message: "S", delay: "1h"}}
	next: {uses: "echo:echo", needs: ["slow"], with: message: "never"}
}
`)
		w, _ := runUntil(t, dir, "slow             failed:")

		out := w.String()
		assert.Contains(t, out, "timed out after 50ms; raise timeout in kevin.cue")
		assert.NotContains(t, out, "next             up", "a dependent of a timed-out step must not start")
		assert.Contains(t, out, "slow             removed", "the cut-off step must get a Down")
	})

	t.Run("leaves a step that finishes in time alone", func(t *testing.T) {
		dir := project(t, `
env: ok: {uses: "echo:echo", timeout: "1m", with: message: "A"}
`)
		w, err := runUntil(t, dir, "ok               ready")
		require.NoError(t, err)
		assert.NotContains(t, w.String(), "failed:")
	})
}

func TestRerunStep(t *testing.T) {
	t.Run("rejects an unknown step without recording it", func(t *testing.T) {
		store := session.NewStore()
		r := &run{store: store, steps: map[string]config.Step{"web": {}}}

		err := r.RerunStep(t.Context(), "nope", false)

		require.ErrorIs(t, err, session.ErrUnknownStep)
		assert.Empty(t, store.Snapshot().Steps)
	})
}

func TestWarnDenied(t *testing.T) {
	start := time.Now()
	newRun := func(requests ...session.Request) (*run, *bytes.Buffer) {
		var events bytes.Buffer
		store := session.NewStore()
		for _, req := range requests {
			store.Record(req)
		}
		return &run{store: store, events: &events}, &events
	}

	t.Run("names each denied host once", func(t *testing.T) {
		r, events := newRun(
			session.Request{Time: start.Add(time.Second), Denied: true, Host: "registry-1.docker.io"},
			session.Request{Time: start.Add(2 * time.Second), Denied: true, Host: "registry-1.docker.io"},
			session.Request{Time: start.Add(3 * time.Second), Denied: true, Host: "auth.docker.io"},
		)

		r.warnDenied(t.Context(), "cluster", start)

		out := events.String()
		assert.Contains(t, out, "the proxy denied requests to registry-1.docker.io, auth.docker.io while cluster was starting")
		assert.Contains(t, out, "proxy.egress.allow")
	})

	t.Run("ignores requests that were allowed or came before start", func(t *testing.T) {
		r, events := newRun(
			session.Request{Time: start.Add(-time.Second), Denied: true, Host: "stale.example.com"},
			session.Request{Time: start.Add(time.Second), Host: "allowed.example.com"},
		)

		r.warnDenied(t.Context(), "cluster", start)

		assert.Empty(t, events.String())
	})

	t.Run("a canceled run is not a denial problem", func(t *testing.T) {
		r, events := newRun(session.Request{Time: start.Add(time.Second), Denied: true, Host: "registry-1.docker.io"})
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		r.warnDenied(ctx, "cluster", start)

		assert.Empty(t, events.String())
	})
}

func TestInterruptedSteps(t *testing.T) {
	t.Run("a step that started and never completed", func(t *testing.T) {
		r := &run{}
		r.markStarted("a")

		assert.Equal(t, []string{"a"}, r.snapshotInterrupted())
	})

	t.Run("a step whose Up failed on its own is left to its plugin", func(t *testing.T) {
		r := &run{}
		r.markStarted("a")
		r.markSettled("a")

		assert.Empty(t, r.snapshotInterrupted())
	})

	t.Run("a completed step is not interrupted, even when a rerun started it again", func(t *testing.T) {
		r := &run{}
		r.markStarted("a")
		r.mergeCompleted(map[string]dag.Outputs{"a": {"k": "v"}})
		r.markStarted("a")

		assert.Empty(t, r.snapshotInterrupted())
	})

	t.Run("a step that never called Up", func(t *testing.T) {
		r := &run{}
		r.mergeCompleted(map[string]dag.Outputs{"a": nil})

		assert.Empty(t, r.snapshotInterrupted())
	})
}

// TestRunCrossScopeNeeds covers an env step's needs naming a setup-scope
// step via the "setup.<name>" prefix, resolved through Export rather than
// Up - and each way that resolution can fail.
func TestRunCrossScopeNeeds(t *testing.T) {
	t.Run("resolves a setup step's Export into needs and Deps", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: cluster: {uses: "echo:echo", with: {export: {greeting: "from-setup", password: "hunter2"}, export_sensitive: ["password"]}}
env:   app:     {uses: "echo:echo", needs: ["setup.cluster"], with: message: "${setup.cluster.out.greeting}"}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "app", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "app", "ready"))

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		out := string(logs)
		assert.Contains(t, out, "from-setup", "the CEL-rendered with block must carry the setup step's exported value")
		assert.Contains(t, out, "saw setup.cluster:", "the wire Deps key must be the \"setup.\"-prefixed name")
		// echo logs req.Deps with %v, and plugin.Sensitive's String() redacts
		// to "[REDACTED]" - proving the Sensitive flag reached the plugin
		// (not just that the raw value did) exactly as export_sensitive named it.
		assert.Contains(t, out, "password:[REDACTED]", "export_sensitive must keep its Sensitive flag crossing scopes via Deps")
		assert.NotContains(t, out, "hunter2", "a Sensitive value must never appear in its raw form in the log")
		assert.Contains(t, out, "greeting:from-setup", "a non-sensitive value must appear in the clear")
	})

	t.Run("renders a setup step's own with block before exporting it", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: cluster: {uses: "echo:echo", with: export: cert: "${project.root_cert}"}
env:   app:     {uses: "echo:echo", needs: ["setup.cluster"], with: message: "${setup.cluster.out.cert}"}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "app", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "app", "ready"))

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		assert.Contains(t, string(logs), "root.crt",
			"the setup step's own with block must be rendered before Export sees it, not sent as the literal ${project.root_cert} template")
	})

	t.Run("memoizes Export across concurrent consumers of the same setup step", func(t *testing.T) {
		requireRelay(t)
		dir := project(t, `
setup: cluster: {uses: "echo:echo", with: export: greeting: "from-setup"}
env: {
	app1: {uses: "echo:echo", needs: ["setup.cluster"], with: message: "calls=${setup.cluster.out.export_calls}"}
	app2: {uses: "echo:echo", needs: ["setup.cluster"], with: message: "calls=${setup.cluster.out.export_calls}"}
	join: {uses: "echo:echo", needs: ["app1", "app2"]}
}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "join", "ready"))
		require.NoError(t, err)
		assert.Contains(t, w.String(), fmt.Sprintf("%-16s %s", "join", "ready"))

		logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
		require.NoError(t, err)
		out := string(logs)
		// Up's two concurrent consumers share one call ("calls=1", seen
		// twice); Down re-renders the same with block and, being a
		// separate, later, non-concurrent phase, correctly triggers a
		// fresh one ("calls=2", also seen twice, once per consumer) -
		// singleflight dedupes concurrent callers, it does not cache
		// across the whole run the way the previous sync.OnceValues did
		// (that caching is what let a canceled request permanently poison
		// a later, unrelated call - see TestExportCrossScopeStepRetriesAfterFailure).
		assert.Equal(t, 2, strings.Count(out, "calls=1"), "up's two consumers must share one Export call")
		assert.Equal(t, 2, strings.Count(out, "calls=2"), "down's two consumers must share their own one Export call")
		assert.NotContains(t, out, "calls=3", "no more than one Export call per phase")
	})

	t.Run("an unknown same-scope name fails", func(t *testing.T) {
		dir := project(t, `
env: app: {uses: "echo:echo", needs: ["missing"]}
`)
		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `app: needs "missing": no such step in scope "env"`)
	})

	t.Run("an unknown setup-scope name fails", func(t *testing.T) {
		dir := project(t, `
env: app: {uses: "echo:echo", needs: ["setup.missing"]}
`)
		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `app: needs "setup.missing": no such step in scope "setup"`)
	})

	t.Run("a setup step with no Exporter fails", func(t *testing.T) {
		dir := project(t, `
setup: cluster: {uses: "echo:probe"}
env:   app:     {uses: "echo:echo", needs: ["setup.cluster"]}
`)
		w, err := runUntil(t, dir, fmt.Sprintf("%-16s %s", "app", "failed:"))
		require.Error(t, err)
		assert.Contains(t, w.String(), "does not implement export")
	})

	t.Run("the setup prefix is rejected outside the env scope", func(t *testing.T) {
		dir := project(t, `
setup: {
	a: {uses: "echo:echo", needs: ["setup.b"]}
	b: {uses: "echo:echo"}
}
`)
		err := Run(t.Context(), Options{Dir: dir, Scope: config.ScopeSetup})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `a: needs "setup.b": only an env step can use a "setup." dependency`)
	})
}

// TestRunLeavesTheRelayForAStillLiveOtherScope proves a "kevin setup"
// process (Keep, NoWait) leaves its relay container running once it exits,
// for a later "kevin run" to reuse - the entire point of persisting the
// relay across processes. An unconditional defer that always closed it,
// regardless of shutdown's own keep/otherScopeLive decision, would defeat
// this on every exit path.
func TestRunLeavesTheRelayForAStillLiveOtherScope(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
project: "engine-relay-persist-test"
setup: cluster: {uses: "echo:echo"}
`)
	t.Cleanup(func() {
		_ = dockerClient.Remove(context.WithoutCancel(t.Context()), "kevin-engine-relay-persist-test-relay")
		_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), NetworkName("engine-relay-persist-test"))
	})

	require.NoError(t, Run(t.Context(), Options{Dir: dir, Scope: config.ScopeSetup, Keep: true, NoWait: true}))

	_, err := dockerClient.Inspect(t.Context(), "kevin-engine-relay-persist-test-relay")
	require.NoError(t, err, "kevin setup's relay must survive once the setup process exits")
}

// TestExportCrossScopeStepRetriesAfterFailure proves a failed
// exportCrossScopeStep call - such as one whose context a dropped
// console/MCP rerun request canceled mid-flight - never poisons a later,
// independent call for the same setup step. singleflight.Group forgets a
// call once it completes, unlike the sync.OnceValues this once used, which
// would have cached the failure for the rest of the run.
func TestExportCrossScopeStepRetriesAfterFailure(t *testing.T) {
	dir := project(t, `
setup: cluster: {uses: "echo:echo", with: export: greeting: "from-setup"}
`)
	cfg, plugins, caps, err := LoadAndLaunch(t.Context(), dir, "", nil, config.VariableInputs{})
	require.NoError(t, err)
	defer CloseAll(plugins)

	r := &run{cfg: cfg, plugins: plugins, caps: caps, env: &pb.Environment{}}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.exportCrossScopeStep(canceledCtx, "cluster")
	require.Error(t, err, "a canceled context must fail the call")

	exported, err := r.exportCrossScopeStep(t.Context(), "cluster")
	require.NoError(t, err, "a later call with a healthy context must not see the earlier failure")
	assert.Equal(t, output.Value{String: "from-setup"}, exported.Outputs["greeting"])
}

// TestTeardownResolvesSameScopeNeeds proves Teardown backfills a setup
// step's Export into another setup step's completed outputs, so a
// same-scope "needs.<name>.out.*" reference in the second step's with
// block still renders during its own Down - Teardown never calls Up, so
// nothing else would populate it.
func TestTeardownResolvesSameScopeNeeds(t *testing.T) {
	dir := project(t, `
setup: {
	cluster: {uses: "echo:echo", with: {outputs: greeting: "from-cluster", export: greeting: "from-cluster"}}
	app:     {uses: "echo:echo", needs: ["cluster"], with: message: "${needs.cluster.out.greeting}"}
}
`)
	require.NoError(t, Run(t.Context(), Options{Dir: dir, Scope: config.ScopeSetup, Keep: true, NoWait: true}))
	require.NoError(t, Teardown(t.Context(), Options{Dir: dir}))

	logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
	require.NoError(t, err)
	assert.Contains(t, string(logs), "from-cluster",
		"app's Down must render needs.cluster.out.greeting, not fail or see it empty")
}

// TestRunRejectsInvalidConfiguration covers every way Run must fail before it
// ever launches a plugin or touches docker: a bad reference, a schema
// violation, a reserved namespace, a filesystem problem. None of these needs
// requireDocker - LoadAndLaunch or prepare's own validation rejects the
// config first.
func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Run("reports a plugin that will not start", func(t *testing.T) {
		dir := configDir(t, proxyBlock(t)+`
plugins: echo: cmd: "/nonexistent/kevin-plugin-echo"
env: a: uses: "echo:echo"
`)
		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "echo")
	})

	t.Run("starts only the plugins that steps reference", func(t *testing.T) {
		dir := configDir(t, proxyBlock(t)+`
plugins: {
	echo:    cmd: "/nonexistent/kevin-plugin-echo"
	unused: cmd: "/nonexistent/kevin-plugin-unused"
}
env: a: uses: "echo:echo"
`)
		err := runEnv(t, dir)
		require.Error(t, err, "echo still fails to start, and that failure must reach the caller")
		assert.Contains(t, err.Error(), "echo")
		assert.NotContains(t, err.Error(), "unused",
			"a declared plugin that no step references must never start")
	})

	t.Run("rejects config the plugin schema does not allow", func(t *testing.T) {
		dir := project(t, `
env: a: {uses: "echo:echo", with: nonsense: true}
`)

		w := &watcher{}
		err := Run(t.Context(), Options{
			Dir:    dir,
			Scope:  config.ScopeEnv,
			Events: w,
		})
		require.ErrorIs(t, err, config.ErrInvalid)
		assert.Contains(t, err.Error(), "env.a.with")
		assert.Empty(t, w.String(), "no step can run when the environment is not valid")
	})

	t.Run("rejects a reserved namespace before launching anything", func(t *testing.T) {
		dir := configDir(t, proxyBlock(t)+`
plugins: "kevin": cmd: "echo"
env: a: uses: "kevin:thing"
`)
		err := runEnv(t, dir)
		require.ErrorIs(t, err, config.ErrReservedNamespace,
			"a reserved namespace must fail before the engine ever launches the entry")
	})

	t.Run("reports a plugin name mismatch", func(t *testing.T) {
		bin, err := echoPlugin()
		require.NoError(t, err)
		dir := configDir(t, proxyBlock(t)+"plugins: notecho: cmd: "+strconv.Quote(bin)+"\nenv: a: uses: \"notecho:echo\"\n")

		err = runEnv(t, dir)
		require.ErrorIs(t, err, pluginhost.ErrNameMismatch)
		assert.Contains(t, err.Error(), "notecho")
	})

	t.Run("rejects a step naming an undeclared plugin", func(t *testing.T) {
		dir := project(t, `
env: a: uses: "nope:echo"
`)
		require.ErrorIs(t, runEnv(t, dir), config.ErrUnknownPlugin)
	})

	t.Run("reports a missing config file", func(t *testing.T) {
		require.ErrorIs(t, runEnv(t, t.TempDir()), config.ErrNotFound)
	})

	// "reports a workspace creation failure" forces MkdirAll to fail: a file
	// sits where the workspace directory must go.
	t.Run("reports a workspace creation failure", func(t *testing.T) {
		dir := project(t, `env: {}`)
		require.NoError(t, os.WriteFile(filepath.Join(dir, WorkspaceDir), []byte("not a directory"), 0o600))

		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), WorkspaceDir)
	})
}

// TestRunAppliesEgressFromConfigToTheProxy proves proxy.egress.allow in
// kevin.cue reaches the running proxy, and that deny (unset here, so
// proxyBlock's own default true applies) still blocks everything else.
func TestRunAppliesEgressFromConfigToTheProxy(t *testing.T) {
	requireRelay(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "reachable")
	}))
	t.Cleanup(target.Close)

	addr := freeAddr(t)
	dir := project(t, `
proxy: {
	listen: "`+addr+`"
	egress: allow: ["127.0.0.1"]
}
env: a: {uses: "echo:echo", with: message: "A"}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The watcher normally cancels the run context when the step comes up.
	// Here it only unblocks the test, so the checks below run against a
	// proxy that is still up.
	ready := make(chan struct{})
	w := &watcher{until: "a                ready", cancel: func() { close(ready) }}
	done := runAsync(t, ctx, dir, w)

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("the environment never came up")
	}

	proxyURL, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}

	resp, err := getVia(t, client, target.URL+"/")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "reachable", string(body), "the allow list in kevin.cue must reach the proxy")

	resp, err = getVia(t, client, "http://denied.kevin.test/")
	require.NoError(t, err)
	body, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"proxyBlock's own deny:true default must reach the proxy")
	assert.Contains(t, string(body), "Blocked by kevin")

	cancel()
	require.NoError(t, <-done)
}

// TestRunAppliesMaxParallelFromConfig proves engine.max_parallel in
// kevin.cue reaches the DAG walk: two independent steps that would normally
// overlap must instead run one after the other.
func TestRunAppliesMaxParallelFromConfig(t *testing.T) {
	requireRelay(t)

	dir := project(t, `
engine: max_parallel: 1
env: {
	a: {uses: "echo:echo", with: delay: "300ms"}
	b: {uses: "echo:echo", with: delay: "300ms"}
	c: {uses: "echo:echo", needs: ["a", "b"]}
}
`)

	start := time.Now()
	_, err := runUntil(t, dir, "c                ready")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 550*time.Millisecond,
		"max_parallel: 1 must serialize a and b instead of overlapping their delays")
}

// TestRunRendersNeedsTemplatesBeforeDown proves down renders a step's with
// block against upstream outputs exactly like up does, rather than sending
// the plugin the raw "${needs...}" template string.
func TestRunRendersNeedsTemplatesBeforeDown(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a: {uses: "echo:echo", with: {message: "A", outputs: greeting: "hi"}}
	b: {uses: "echo:echo", needs: ["a"], with: message: "bye ${needs.a.out.greeting}"}
}
`)
	w, err := runUntil(t, dir, "b                ready")
	require.NoError(t, err)
	assert.Contains(t, w.String(), "b                removed")

	// A step's own log lines go to the durable log file, not the terminal.
	logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
	require.NoError(t, err)
	assert.Contains(t, string(logs), "bye hi",
		"Down must receive the rendered value, not the raw needs.* template")
}

// TestRunRendersProjectTemplates proves a with block can read the
// project.* CEL scope, alongside needs.*, and that it resolves to the
// real host path kevin's own ca.RootCertPath computes - not a stand-in
// or the literal template.
func TestRunRendersProjectTemplates(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a: {uses: "echo:echo", with: message: "cert=${project.root_cert}"}
}
`)
	_, err := runUntil(t, dir, "a                ready")
	require.NoError(t, err)

	logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
	require.NoError(t, err)
	assert.Contains(t, string(logs), "cert="+ca.RootCertPath(),
		"Up must receive the rendered value, not the raw project.* template")
}

// TestRunProjectRelayIsPopulated proves project.relay renders to the
// project's relay container's own address, not an empty string or the
// literal template.
func TestRunProjectRelayIsPopulated(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a: {uses: "echo:echo", with: message: "relay=${project.relay}"}
}
`)
	_, err := runUntil(t, dir, "a                ready")
	require.NoError(t, err)

	logs, err := os.ReadFile(filepath.Join(dir, WorkspaceDir, LogsFile))
	require.NoError(t, err)
	assert.Regexp(t, `relay=\S+`, string(logs),
		"project.relay must render to the relay container's own address, not go empty")
	assert.NotContains(t, string(logs), "${project.relay}",
		"Up must receive the rendered value, not the raw project.* template")
}

// TestRunExportStepRendersNeedsTemplates proves export_step renders a
// step's with block against upstream outputs, rather than sending the
// plugin the raw "${needs...}" template string.
func TestRunExportStepRendersNeedsTemplates(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a: {uses: "echo:echo", with: outputs: greeting: "hi"}
	b: {uses: "echo:echo", needs: ["a"], with: export: greeting: "${needs.a.out.greeting}"}
}
`)
	w := &watcher{}
	addrCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Dir: dir, Scope: config.ScopeEnv, Events: w,
			OnEnvironment: func(env *pb.Environment) { addrCh <- env.GetConsoleAddr() },
		})
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the environment address")
	}
	waitForCount(t, w, "b                ready", 1, 5*time.Second)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + addr + mcpserver.Path,
	}, nil)
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	result, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "export_step",
		Arguments: map[string]string{"name": "b"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "%v", result.Content)

	out, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok, "expected structured content, got %#v", result.StructuredContent)
	rows, ok := out["out"].([]any)
	require.True(t, ok, "expected an out array, got %#v", out["out"])

	var greeting any
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if row["label"] == "greeting" {
			greeting = row["value"]
		}
	}
	assert.Equal(t, "hi", greeting, "export_step must receive the rendered value, not the raw needs.* template")

	cancel()
	require.NoError(t, <-done)
}

// TestRunCallsAPluginDeclaredTool proves a plugin-declared MCP tool
// (echo's ToolProvider) reaches the plugin through a real tools/call,
// with the step argument resolved to that step's rendered config and deps.
func TestRunCallsAPluginDeclaredTool(t *testing.T) {
	requireRelay(t)
	dir := project(t, `
env: {
	a: {uses: "echo:echo", with: outputs: greeting: "hi"}
	b: {uses: "echo:echo", needs: ["a"], with: message: "hello"}
}
`)
	w := &watcher{}
	addrCh := make(chan string, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Dir: dir, Scope: config.ScopeEnv, Events: w,
			OnEnvironment: func(env *pb.Environment) { addrCh <- env.GetConsoleAddr() },
		})
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the environment address")
	}
	waitForCount(t, w, "b                ready", 1, 5*time.Second)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: "http://" + addr + mcpserver.Path,
	}, nil)
	require.NoError(t, err)
	defer func() { _ = sess.Close() }()

	tools, err := sess.ListTools(t.Context(), nil)
	require.NoError(t, err)
	var toolName string
	for _, tl := range tools.Tools {
		if strings.HasPrefix(tl.Name, "echo_echo_") {
			toolName = tl.Name
		}
	}
	require.NotEmpty(t, toolName, "echo's step type must advertise its tool, namespaced echo_echo_<tool>")

	result, err := sess.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      toolName,
		Arguments: map[string]any{"step": "b"},
	})
	require.NoError(t, err)
	require.False(t, result.IsError, "%v", result.Content)

	out, ok := result.StructuredContent.(map[string]any)
	require.True(t, ok, "expected structured content, got %#v", result.StructuredContent)
	assert.Equal(t, "hello", out["message"])
	deps, ok := out["deps"].(map[string]any)
	require.True(t, ok, "expected a deps object, got %#v", out["deps"])
	require.Contains(t, deps, "a")

	cancel()
	require.NoError(t, <-done)
}

// TestRunRerunReExecutesAStepAndFillsInSkippedDependents proves the
// console's rerun endpoint reaches run.RerunStep end to end. A dependent
// that a failure left skipped shows as skipped on the page; a direct rerun
// (cascade=false) re-executes its target even though echoStep never
// declares itself idempotent - direct targeting always runs, regardless of
// the idempotent flag; a cascading rerun of the failed step (cascade=true)
// sweeps the skipped dependent back in, attempting it again rather than
// leaving it stuck on the original failure.
func TestRunRerunReExecutesAStepAndFillsInSkippedDependents(t *testing.T) {
	requireRelay(t)

	consoleAddr := freeAddr(t)
	dir := project(t, `
console: listen: "`+consoleAddr+`"
env: {
	a:    {uses: "echo:echo", with: message: "A"}
	boom: {uses: "echo:echo", needs: ["a"], with: fail: true}
	next: {uses: "echo:echo", needs: ["boom"], with: message: "never"}
}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{}
	done := runAsync(t, ctx, dir, w)

	waitForCount(t, w, "boom             failed:", 1, 30*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	consoleURL := "http://" + consoleAddr

	page := getPage(t, client, consoleURL)
	assert.Contains(t, page, `id="step-next"`)
	assert.Contains(t, page, "state-skipped", "a dependent of a failed step must show as skipped")

	postRerun(t, client, consoleURL, "a", false)
	waitForCount(t, w, "a                ready", 2, 5*time.Second)

	postRerun(t, client, consoleURL, "boom", true)
	waitForCount(t, w, "boom             failed:", 2, 5*time.Second)
	assert.NotContains(t, w.String(), "next             up", "next must never actually run")

	cancel()
	require.Error(t, <-done)
}

// TestRunRerunWhileAnotherStepIsStillUp proves a rerun sees a dependency's
// real output even while some unrelated step is still blocked inside its
// own Up call. r.completed used to land in bulk only once every step in
// the walk returned, so a rerun requested during that window saw no prior
// output for anything - not just the still-running step - and marked its
// target Skipped instead of actually rerunning it.
func TestRunRerunWhileAnotherStepIsStillUp(t *testing.T) {
	requireRelay(t)

	consoleAddr := freeAddr(t)
	dir := project(t, `
console: listen: "`+consoleAddr+`"
env: {
	a:    {uses: "echo:echo", with: message: "A"}
	b:    {uses: "echo:echo", needs: ["a"], with: message: "B"}
	hold: {uses: "echo:echo", needs: ["b"], with: delay: "3s"}
}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{}
	done := runAsync(t, ctx, dir, w)

	// hold is now blocked inside its own 3s Up call - the walk that used to
	// be the only source of r.completed has not returned yet.
	waitForCount(t, w, fmt.Sprintf("%-16s %s", "hold", "up"), 1, 10*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	postRerun(t, client, "http://"+consoleAddr, "b", false)
	waitForCount(t, w, fmt.Sprintf("%-16s %s", "b", "ready"), 2, 3*time.Second)

	cancel()
	<-done
}

// TestRunRerunDoesNotDuplicateAStepsCardDetails proves upStep's
// ClearStepDetails call keeps a rerun from piling a second copy of a step's
// detail rows onto the first - a step's Up can only republish what is still
// true, it has no way to say a prior row no longer applies. A direct rerun
// runs its target regardless of idempotence, so this needs no idempotent
// step type to exercise.
func TestRunRerunDoesNotDuplicateAStepsCardDetails(t *testing.T) {
	requireRelay(t)

	consoleAddr := freeAddr(t)
	dir := project(t, `
console: listen: "`+consoleAddr+`"
env: {
	a: {uses: "echo:echo", with: details: [{label: "admin password", value: "hunter2", copyable: true}]}
}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{}
	done := runAsync(t, ctx, dir, w)

	waitForCount(t, w, "a                ready", 1, 30*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	consoleURL := "http://" + consoleAddr

	page := getPage(t, client, consoleURL)
	before := strings.Count(page, "hunter2")
	assert.Positive(t, before, "the detail row must appear on the card at least once")

	postRerun(t, client, consoleURL, "a", false)
	waitForCount(t, w, "a                ready", 2, 5*time.Second)

	page = getPage(t, client, consoleURL)
	assert.Equal(t, before, strings.Count(page, "hunter2"),
		"a rerun must not pile a second copy of the same detail row onto the card")

	cancel()
	require.NoError(t, <-done)
}

// TestRunPopulatesStepInputsAndOutputs proves a step's resolved with block
// and published outputs reach the console's detail dialog: b's rendered
// with value (not the raw "${needs...}" template) and a's published output
// both show up on the page. It also proves a's "secret" field - a plain
// literal with no "${needs...}" marker to trace, but declared @sensitive()
// in echo's schema.cue - is still redacted, closing the gap
// expr.FieldSensitive alone can't: nothing to trace a literal back to.
func TestRunPopulatesStepInputsAndOutputs(t *testing.T) {
	requireRelay(t)

	consoleAddr := freeAddr(t)
	dir := project(t, `
console: listen: "`+consoleAddr+`"
env: {
	a: {uses: "echo:echo", with: {message: "A", secret: "sh0uldb3hidden", outputs: greeting: "hi"}}
	b: {uses: "echo:echo", needs: ["a"], with: message: "hello ${needs.a.out.greeting}"}
}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{}
	done := runAsync(t, ctx, dir, w)

	waitForCount(t, w, "b                ready", 1, 30*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	page := getPage(t, client, "http://"+consoleAddr)

	assert.Contains(t, page, ">greeting<", "a's outputs panel must show its published output's key")
	assert.Contains(t, page, `title="hi"`, "a's outputs panel must show its published output's value")
	assert.Contains(t, page, ">message<", "b's inputs panel must show its with block's field name")
	assert.Contains(t, page, `title="hello hi"`,
		"b's inputs panel must show the rendered with value, not the raw needs.* template")
	assert.Contains(t, page, ">secret<", "a's inputs panel must still show the field's label")
	assert.NotContains(t, page, "sh0uldb3hidden",
		"a schema-declared @sensitive() field must be redacted even as a literal with no marker to trace")

	cancel()
	require.NoError(t, <-done)
}

// TestRunResolvesVariables proves a declared variables: entry reaches
// "${vars.<name>}" in a with block, sourced from Options.Vars (the "--var"
// equivalent), and that a field referencing a variable variables: marks
// sensitive is redacted in the console even though the field itself
// carries no schema-level @sensitive() attribute of its own.
func TestRunResolvesVariables(t *testing.T) {
	requireRelay(t)

	consoleAddr := freeAddr(t)
	dir := project(t, `
console: listen: "`+consoleAddr+`"
variables: {
	greeting: default: "bye"
	token: sensitive: true
}
env: {
	a: {uses: "echo:echo", with: {message: "${vars.greeting}", export: token: "${vars.token}"}}
}
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := &watcher{}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{
			Dir: dir, Scope: config.ScopeEnv, Events: w,
			Vars: []string{"token=s3cr3t-token"},
		})
	}()

	waitForCount(t, w, "a                ready", 1, 30*time.Second)

	client := &http.Client{Timeout: 5 * time.Second}
	page := getPage(t, client, "http://"+consoleAddr)

	assert.Contains(t, page, `title="bye"`, "the message field must show vars.greeting's declared default")
	assert.Contains(t, page, ">export<", "the export field's label must still show")
	assert.NotContains(t, page, "s3cr3t-token",
		"a field referencing a sensitive variable must be redacted, even with no @sensitive() of its own")

	cancel()
	require.NoError(t, <-done)
}

// TestRunRemovesAnOrphanContainerAndTheNetwork proves reap finds a container
// a crashed plugin left behind by its labels alone, and removes it along
// with the project's network.
func TestRunRemovesAnOrphanContainerAndTheNetwork(t *testing.T) {
	requireRelay(t)

	dir := project(t, `
project: "kevin-reap-test"
env: {}
`)

	orphan := "kevin-reap-test-orphan"
	_, err := dockerClient.Run(t.Context(), cri.RunSpec{
		Image: "busybox:stable",
		Name:  orphan,
		Cmd:   []string{"sleep", "300"},
		Labels: map[string]string{
			cri.LabelProject: "kevin-reap-test",
			cri.LabelURN:     cri.URNLabel("kevin-reap-test", config.ScopeEnv, "orphan"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dockerClient.Remove(context.WithoutCancel(t.Context()), orphan) })

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	w := &watcher{}
	require.NoError(t, Run(ctx, Options{
		Dir:    dir,
		Scope:  config.ScopeEnv,
		Events: w,
	}))

	names, err := dockerClient.ListByLabel(t.Context(), cri.LabelProject, "kevin-reap-test")
	require.NoError(t, err)
	assert.Empty(t, names, "the orphan must be gone")

	_, err = dockerClient.Inspect(t.Context(), orphan)
	require.ErrorIs(t, err, cri.ErrNotFound)

	assert.Contains(t, w.String(), orphan,
		"the report must name the container, not its ID")
}

// TestRunRemovesTheNetworkWhenRelayFailsToStart proves that Run cleans up the
// docker network it already created even when a later startup step - here,
// the relay - fails before any step ever comes up. Without this, Run
// returned the bare error and left the network behind: nothing ever reaps
// it, since each failed run used a fresh, one-off project name.
func TestRunRemovesTheNetworkWhenRelayFailsToStart(t *testing.T) {
	requireDocker(t)
	t.Setenv(relay.ImageEnvVar, "INVALID/UPPERCASE:tag")

	const projectName = "kevin-run-removes-network-on-relay-failure-test"
	dir := project(t, `
project: "`+projectName+`"
env: a: {uses: "echo:echo", with: message: "A"}
`)
	require.Error(t, runEnv(t, dir))

	_, err := dockerClient.NetworkGateway(t.Context(), NetworkName(projectName))
	require.ErrorIs(t, err, cri.ErrNotFound,
		"Run must remove the network it created when the relay never starts")
}

// TestRunStopsTheRelayWhenStartupFailsAfterwards proves the same cleanup
// reaches a relay that already started: here the console's own listener
// fails to bind (its address is already taken) after the relay is up but
// before Run commits to its steady state, so shutdown must stop the
// still-running relay before reap removes the network - a relay left
// attached would make that removal fail.
func TestRunStopsTheRelayWhenStartupFailsAfterwards(t *testing.T) {
	requireRelay(t)

	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })

	bin, err := echoPlugin()
	require.NoError(t, err)

	const projectName = "kevin-run-stops-relay-on-later-failure-test"
	_, gatewayPort, err := net.SplitHostPort(freeAddr(t))
	require.NoError(t, err)
	dir := configDir(t, "plugins: echo: cmd: "+strconv.Quote(bin)+"\n"+
		"project: "+strconv.Quote(projectName)+"\n"+
		"proxy: {listen: "+strconv.Quote(freeAddr(t))+", gateway_port: "+gatewayPort+", egress: deny: true}\n"+
		"console: listen: "+strconv.Quote(busy.Addr().String())+"\n"+
		`env: a: {uses: "echo:echo", with: message: "A"}`+"\n")

	require.Error(t, runEnv(t, dir))

	_, err = dockerClient.Inspect(t.Context(), "kevin-"+projectName+"-relay")
	require.ErrorIs(t, err, cri.ErrNotFound, "Run must stop the relay it already started")

	_, err = dockerClient.NetworkGateway(t.Context(), NetworkName(projectName))
	require.ErrorIs(t, err, cri.ErrNotFound, "Run must remove the network once the relay is stopped")
}

// TestRunOnEvent covers what Run's real plugins never trigger through the
// black-box scenarios above: a progress event with a nonzero total. The echo
// fixture plugin always reports Total: 0.
func TestRunOnEvent(t *testing.T) {
	tests := []struct {
		name string
		ev   *pb.Event
		want string
	}{
		{
			// A log line goes to the console and the durable log file, not
			// the terminal writer.
			name: "a log line",
			ev:   &pb.Event{Event: &pb.Event_Log{Log: &pb.LogLine{Text: "hello"}}},
			want: "",
		},
		{
			name: "progress with no total",
			ev:   &pb.Event{Event: &pb.Event_Progress{Progress: &pb.Progress{Label: "waiting"}}},
			want: fmt.Sprintf("%-16s %s\n", "step", "waiting"),
		},
		{
			name: "progress with a total",
			ev:   &pb.Event{Event: &pb.Event_Progress{Progress: &pb.Progress{Label: "copying", Current: 3, Total: 10}}},
			want: fmt.Sprintf("%-16s %s\n", "step", "copying (3/10)"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			r := &run{
				events:  &buf,
				store:   session.NewStore(),
				stepLog: slog.New(slog.DiscardHandler),
			}
			r.onEvent("step")(tt.ev)
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestOutputsFromProto(t *testing.T) {
	assert.Nil(t, outputsFromProto(nil))
	assert.Nil(t, outputsFromProto(&pb.Outputs{}))
	assert.Equal(t,
		dag.Outputs{"k": output.Value{String: "v"}, "secret": output.Value{String: "s", Sensitive: true}},
		outputsFromProto(&pb.Outputs{Values: map[string]*pb.Value{
			"k":      {Kind: &pb.Value_StringValue{StringValue: "v"}},
			"secret": {Kind: &pb.Value_StringValue{StringValue: "s"}, Sensitive: true},
		}}),
	)
}

func TestOutputsToProto(t *testing.T) {
	assert.Nil(t, outputsToProto(nil))
	assert.Nil(t, outputsToProto(dag.Outputs{}))
	assert.Equal(t,
		&pb.Outputs{Values: map[string]*pb.Value{
			"k":      {Kind: &pb.Value_StringValue{StringValue: "v"}},
			"secret": {Kind: &pb.Value_StringValue{StringValue: "s"}, Sensitive: true},
		}},
		outputsToProto(dag.Outputs{"k": output.Value{String: "v"}, "secret": output.Value{String: "s", Sensitive: true}}),
	)
}

func TestInputRows(t *testing.T) {
	t.Run("no with block reports no rows", func(t *testing.T) {
		rows, err := inputRows(nil, nil, expr.Scopes{}, nil, nil)
		require.NoError(t, err)
		assert.Nil(t, rows)
	})

	t.Run("one row per top-level field, sorted, with the rendered value, and a copy button since neither is sensitive", func(t *testing.T) {
		raw := json.RawMessage(`{"image":"postgres:16","port":"${needs.db.out.port}"}`)
		rendered := json.RawMessage(`{"image":"postgres:16","port":"5432"}`)
		rows, err := inputRows(raw, rendered, expr.Scopes{Needs: map[string]dag.Outputs{
			"db": {"port": output.Value{String: "5432"}},
		}}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{
			{Label: "image", Value: "postgres:16", Copyable: true},
			{Label: "port", Value: "5432", Copyable: true},
		}, rows)
	})

	t.Run("a field referencing a sensitive value is marked Sensitive and not Copyable", func(t *testing.T) {
		raw := json.RawMessage(`{"password":"${needs.db.out.password}"}`)
		rendered := json.RawMessage(`{"password":"hunter2"}`)
		rows, err := inputRows(raw, rendered, expr.Scopes{Needs: map[string]dag.Outputs{
			"db": {"password": output.Value{String: "hunter2", Sensitive: true}},
		}}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a field the schema marks sensitive is redacted even as a literal with no marker at all", func(t *testing.T) {
		raw := json.RawMessage(`{"password":"hunter2"}`)
		rows, err := inputRows(raw, raw, expr.Scopes{}, map[string]bool{"password": true}, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a field referencing a sensitive variable is redacted", func(t *testing.T) {
		raw := json.RawMessage(`{"token":"${vars.api_key}"}`)
		rendered := json.RawMessage(`{"token":"sk-123"}`)
		rows, err := inputRows(raw, rendered, expr.Scopes{Vars: map[string]any{"api_key": "sk-123"}}, nil, map[string]bool{"api_key": true})
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "token", Value: "sk-123", Sensitive: true, Copyable: false}}, rows)
	})

	t.Run("a non-string field keeps its compact JSON form", func(t *testing.T) {
		raw := json.RawMessage(`{"ports":[5432,5433]}`)
		rows, err := inputRows(raw, raw, expr.Scopes{}, nil, nil)
		require.NoError(t, err)
		assert.Equal(t, []session.Detail{{Label: "ports", Value: "[5432,5433]", Copyable: true}}, rows)
	})
}

// TestValidateRenderedWith proves renderWith's post-render schema check
// (validateRenderedWith) catches a rendered with block that doesn't
// actually satisfy its step type's schema - the case config-validate time
// couldn't yet, since a bare "${...}" marker's real value (a declared
// variable's, or a needs/setup-sourced one) is only known once it's
// rendered. A with block with no marker at all never reaches this check
// with anything new to verify, since config-validate time already fully
// checked it.
func TestValidateRenderedWith(t *testing.T) {
	caps := map[string]pluginhost.Info{
		"myplugin": {Steps: []pluginhost.StepInfo{
			{Name: "mystep", Schema: []byte(`#Config: {port: int}`)},
		}},
	}
	step := config.Step{Uses: "myplugin:mystep", With: json.RawMessage(`{"port":"${vars.port}"}`)}

	t.Run("a rendered value matching the schema passes", func(t *testing.T) {
		r := &run{caps: caps}
		err := r.validateRenderedWith("app", step, json.RawMessage(`{"port":3}`))
		require.NoError(t, err)
	})

	t.Run("a rendered value of the wrong type fails", func(t *testing.T) {
		r := &run{caps: caps}
		err := r.validateRenderedWith("app", step, json.RawMessage(`{"port":"not-an-int"}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "app.with")
	})

	t.Run("a with block with no marker at all skips the check", func(t *testing.T) {
		r := &run{caps: caps}
		noMarker := config.Step{Uses: "myplugin:mystep", With: json.RawMessage(`{"port":3}`)}
		err := r.validateRenderedWith("app", noMarker, json.RawMessage(`{"port":"not-an-int"}`))
		require.NoError(t, err, "no marker means config-validate time already fully checked this field")
	})

	t.Run("an unknown plugin skips the check", func(t *testing.T) {
		r := &run{caps: map[string]pluginhost.Info{}}
		err := r.validateRenderedWith("app", step, json.RawMessage(`{"port":"not-an-int"}`))
		require.NoError(t, err)
	})
}

func TestSensitiveWithFields(t *testing.T) {
	t.Run("no schema reports no fields", func(t *testing.T) {
		fields, err := sensitiveWithFields(nil)
		require.NoError(t, err)
		assert.Nil(t, fields)
	})

	t.Run("only a @sensitive() field is reported", func(t *testing.T) {
		schema := []byte(`#Config: {
			image?: string
			password?: string @sensitive()
		}`)
		fields, err := sensitiveWithFields(schema)
		require.NoError(t, err)
		assert.Equal(t, map[string]bool{"password": true}, fields)
	})

	t.Run("a malformed schema errors", func(t *testing.T) {
		_, err := sensitiveWithFields([]byte(`#Config: {`))
		require.Error(t, err)
	})
}

func TestOutputRows(t *testing.T) {
	t.Run("no outputs reports no rows", func(t *testing.T) {
		assert.Nil(t, outputRows(nil))
	})

	t.Run("one row per output, sorted, sensitive rows not copyable", func(t *testing.T) {
		rows := outputRows(map[string]output.Value{
			"endpoint": {String: "localhost:55432"},
			"password": {String: "hunter2", Sensitive: true},
		})
		assert.Equal(t, []session.Detail{
			{Label: "endpoint", Value: "localhost:55432", Copyable: true},
			{Label: "password", Value: "hunter2", Sensitive: true, Copyable: false},
		}, rows)
	})
}

func TestProxyEnvKeepsInternalTrafficOffTheProxy(t *testing.T) {
	env := ProxyEnv("127.0.0.1:18080", "kevin-demo", []string{"web", "api"})

	endpoint := "http://" + HostGateway + ":18080"
	assert.Equal(t, endpoint, env["HTTP_PROXY"])
	assert.Equal(t, endpoint, env["HTTPS_PROXY"])
	assert.Equal(t, env["HTTP_PROXY"], env["http_proxy"], "some images read the lowercase name only")

	// A workload reaches the proxy on the gateway of the host, not on its own
	// loopback.
	assert.NotContains(t, env["HTTP_PROXY"], "127.0.0.1")

	for _, direct := range []string{"localhost", "127.0.0.1", "kevin-demo", ".svc", ".cluster.local"} {
		assert.Contains(t, env["NO_PROXY"], direct,
			"%s must not loop back through the proxy", direct)
	}

	// A step reaches another by step name on the docker network. The proxy
	// runs outside that network and cannot resolve the name.
	for _, step := range []string{"web", "api"} {
		assert.Contains(t, env["NO_PROXY"], step,
			"a workload must reach step %q directly", step)
	}
	assert.Equal(t, env["NO_PROXY"], env["no_proxy"])
}

func TestNetworkNameCarriesTheProject(t *testing.T) {
	assert.Equal(t, "kevin-demo", NetworkName("demo"))
	assert.NotEqual(t, NetworkName("a"), NetworkName("b"))
}

// TestStartProxyGatewayPort proves that opts.GatewayPort is used as-is for
// the gateway listener, and that a port already in use fails outright
// rather than falling back to a different one.
func TestStartProxyGatewayPort(t *testing.T) {
	t.Run("uses the requested port", func(t *testing.T) {
		requireDocker(t)

		cfg := &config.Config{Project: "kevin-gwport-pin-test", Dir: t.TempDir()}
		_, authority, err := prepare(t.Context(), cfg, dockerClient)
		require.NoError(t, err)
		network := NetworkName(cfg.Project)
		t.Cleanup(func() {
			_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
		})

		gateway, err := dockerClient.NetworkGateway(t.Context(), network)
		require.NoError(t, err)

		// Reserve a free port on the gateway address, then free it again -
		// startProxy is asked to bind exactly that port back.
		probe := bindGatewayPort(t, gateway.V4)
		wantPort := mustPort(t, probe.Addr().String())
		require.NoError(t, probe.Close())

		server, err := startProxy(t.Context(), dockerClient, authority, proxyOptions{
			Network:     network,
			Listen:      "127.0.0.1:0",
			GatewayPort: wantPort,
			Domain:      "kevin.test",
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = server.Close() })

		assert.Equal(t, wantPort, mustPort(t, server.gatewayAddr))
	})

	t.Run("fails when the port is already in use", func(t *testing.T) {
		requireDocker(t)

		cfg := &config.Config{Project: "kevin-gwport-conflict-test", Dir: t.TempDir()}
		_, authority, err := prepare(t.Context(), cfg, dockerClient)
		require.NoError(t, err)
		network := NetworkName(cfg.Project)
		t.Cleanup(func() {
			_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
		})

		gateway, err := dockerClient.NetworkGateway(t.Context(), network)
		require.NoError(t, err)

		held := bindGatewayPort(t, gateway.V4)
		defer func() { _ = held.Close() }()
		heldPort := mustPort(t, held.Addr().String())

		var lc net.ListenConfig
		probe, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listen := probe.Addr().String()
		require.NoError(t, probe.Close())

		_, err = startProxy(t.Context(), dockerClient, authority, proxyOptions{
			Network:     network,
			Listen:      listen,
			GatewayPort: heldPort,
			Domain:      "kevin.test",
		})
		require.Error(t, err, "a port already in use must fail, not fall back silently")

		again, err := lc.Listen(t.Context(), "tcp", listen)
		require.NoError(t, err, "a failed startProxy must release its primary listener")
		require.NoError(t, again.Close())
	})
}

// gatewayRuntime is a cri.Runtime double whose network has a fixed gateway.
type gatewayRuntime struct {
	cri.Runtime

	gateway    cri.Gateway
	gatewayErr error
}

func (g gatewayRuntime) NetworkGateway(context.Context, string) (cri.Gateway, error) {
	return g.gateway, g.gatewayErr
}

// freeLoopbackAddr returns a loopback address that nothing listens on.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// TestStartProxyReleasesListeners proves that a failed startProxy leaves no
// listener bound, using a loopback gateway so no docker daemon is needed.
func TestStartProxyReleasesListeners(t *testing.T) {
	t.Setenv(state.UserStateDirEnv, t.TempDir())
	t.Setenv(state.ProjectStateDirEnv, t.TempDir())
	authority, err := ca.NewManager("cwd", "", "proxy-release", ca.Options{}).LoadOrGenerateIntermediate()
	require.NoError(t, err)

	tests := []struct {
		name string
		rt   func(t *testing.T) (gatewayRuntime, int)
	}{
		{
			name: "the gateway lookup fails",
			rt: func(*testing.T) (gatewayRuntime, int) {
				return gatewayRuntime{gatewayErr: assert.AnError}, 0
			},
		},
		{
			name: "the gateway port is already in use",
			rt: func(t *testing.T) (gatewayRuntime, int) {
				t.Helper()
				var lc net.ListenConfig
				held, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
				require.NoError(t, err)
				t.Cleanup(func() { _ = held.Close() })
				return gatewayRuntime{gateway: cri.Gateway{V4: netip.MustParseAddr("127.0.0.1")}}, mustPort(t, held.Addr().String())
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt, port := tt.rt(t)
			listen := freeLoopbackAddr(t)

			_, err := startProxy(t.Context(), rt, authority, proxyOptions{
				Network:     "net",
				Listen:      listen,
				GatewayPort: port,
				Domain:      "kevin.test",
			})
			require.Error(t, err)

			var lc net.ListenConfig
			again, err := lc.Listen(t.Context(), "tcp", listen)
			require.NoError(t, err, "the primary listener must be released")
			require.NoError(t, again.Close())
		})
	}
}

// bindGatewayPort reserves a free port on the gateway address and reports
// it, or skips the test - Docker Desktop's VM on macOS/Windows makes the
// gateway address unbindable from the host at all (EADDRNOTAVAIL), the
// same case startProxy itself falls back on.
func bindGatewayPort(t *testing.T, gateway netip.Addr) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", net.JoinHostPort(gateway.String(), "0"))
	if err != nil && errors.Is(err, syscall.EADDRNOTAVAIL) {
		t.Skip("the gateway address is not bindable from the host here:", err)
	}
	require.NoError(t, err)
	return ln
}

// mustPort parses the port out of addr, failing the test if it doesn't.
func mustPort(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port
}

func TestEventsWriter(t *testing.T) {
	t.Run("prefers a caller-supplied writer", func(t *testing.T) {
		w := &os.File{}
		assert.Same(t, w, eventsWriter(Options{Events: w}, true),
			"a caller-supplied writer wins even when termui would otherwise own the terminal")
		assert.Same(t, w, eventsWriter(Options{Events: w}, false))
	})

	t.Run("discards when live and unset", func(t *testing.T) {
		assert.Equal(t, io.Discard, eventsWriter(Options{}, true),
			"termui's live block already shows this information")
	})

	t.Run("falls back to stderr when not live", func(t *testing.T) {
		assert.Equal(t, os.Stderr, eventsWriter(Options{}, false))
	})
}

func TestRouteModeFromProto(t *testing.T) {
	tests := []struct {
		mode pb.RouteMode
		want proxy.RouteMode
	}{
		{mode: pb.RouteMode_ROUTE_MODE_MITM, want: proxy.RouteModeMITM},
		{mode: pb.RouteMode_ROUTE_MODE_PASSTHROUGH, want: proxy.RouteModePassthrough},
		{mode: pb.RouteMode_ROUTE_MODE_RAW, want: proxy.RouteModeRaw},
		{mode: pb.RouteMode(99), want: proxy.RouteModeMITM},
	}
	for _, tt := range tests {
		t.Run(tt.mode.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, routeModeFromProto(tt.mode))
		})
	}
}

func TestStepContainersFor(t *testing.T) {
	backend := []*pb.ContainerInfo{{Id: "abc", Name: "kevin-demo-backend"}}
	cluster := []*pb.ContainerInfo{{Id: "def", Name: "kevin-demo-cluster-worker"}}

	t.Run("a same-scope need with containers contributes an entry", func(t *testing.T) {
		r := &run{appliedFaults: map[string][]string{}}
		r.recordContainers("backend", backend)

		got := r.stepContainersFor(map[string]dag.Outputs{"backend": {}}, nil)
		require.Len(t, got, 1)
		assert.Equal(t, "backend", got[0].GetStep())
		assert.Equal(t, backend, got[0].GetContainers())
	})

	t.Run("a same-scope need with no containers contributes nothing", func(t *testing.T) {
		r := &run{}
		got := r.stepContainersFor(map[string]dag.Outputs{"waiter": {}}, nil)
		assert.Empty(t, got)
	})

	t.Run("a cross-scope need is prefixed with setup.", func(t *testing.T) {
		r := &run{}
		got := r.stepContainersFor(nil, map[string][]*pb.ContainerInfo{"cluster": cluster})
		require.Len(t, got, 1)
		assert.Equal(t, "setup.cluster", got[0].GetStep())
		assert.Equal(t, cluster, got[0].GetContainers())
	})
}

func TestRecordAndClearFaults(t *testing.T) {
	t.Run("recordFaults replaces a prior entry, not accumulates", func(t *testing.T) {
		r := &run{}
		r.recordFaults("fault", []string{"fault"})
		r.recordFaults("fault", []string{"fault-v2"})
		assert.Equal(t, []string{"fault-v2"}, r.appliedFaults["fault"])
	})

	t.Run("recordFaults with no ids clears the entry", func(t *testing.T) {
		r := &run{appliedFaults: map[string][]string{"fault": {"fault"}}}
		r.recordFaults("fault", nil)
		_, ok := r.appliedFaults["fault"]
		assert.False(t, ok)
	})

	t.Run("clearFaults on a step that applied none never touches the relay", func(t *testing.T) {
		// r.relay is nil here - a call into it would panic, proving
		// clearFaults returns before dereferencing it when there is
		// nothing recorded for name.
		r := &run{}
		r.clearFaults(t.Context(), "never-applied-anything")
	})
}

type fakeForward struct{ closed int }

func (f *fakeForward) Addr() net.Addr { return &net.TCPAddr{} }
func (f *fakeForward) Close() error   { f.closed++; return nil }

func TestCloseStepForwards(t *testing.T) {
	t.Run("closeStepForwards closes only that step's forwards", func(t *testing.T) {
		r := &run{}
		web, db := &fakeForward{}, &fakeForward{}
		r.addForward("web", web)
		r.addForward("db", db)

		r.closeStepForwards("web")
		assert.Equal(t, 1, web.closed)
		assert.Equal(t, 0, db.closed)

		require.NoError(t, r.closeForwards())
		assert.Equal(t, 1, web.closed, "a closed forward is not closed again")
		assert.Equal(t, 1, db.closed)
	})
}
