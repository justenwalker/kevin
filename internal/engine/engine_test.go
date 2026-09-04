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
	"runtime"
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
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/protos/pb"
)

// echoPlugin is the path of the compiled echo plugin. Every test uses it.
var echoPlugin = sync.OnceValues(buildEchoPlugin)

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
// directory.
func project(t *testing.T, body string) string {
	t.Helper()
	bin, err := echoPlugin()
	require.NoError(t, err)

	dir := t.TempDir()
	src := "plugins: echo: cmd: " + strconv.Quote(bin) + "\n" + body
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kevin.cue"), []byte(src), 0o600))
	return dir
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

// relayImageTag matches RelayImageTag in build/main.go.
const relayImageTag = "kevin-relay:dev"

// requireRelay skips a test when Docker does not answer, then makes sure the
// relay image exists - Run always starts the relay now.
func requireRelay(t *testing.T) {
	t.Helper()
	requireDocker(t)
	ensureRelayImage(t)
}

// ensureRelayImage builds the relay image from source when it is absent, the
// same way as the relay-image build target. It skips the test when it
// cannot build the image.
func ensureRelayImage(t *testing.T) {
	t.Helper()

	check := exec.CommandContext(t.Context(), "docker", "image", "inspect", relayImageTag)
	if check.Run() == nil {
		return
	}

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Skip("cannot locate the repository root to build the relay image")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")

	dir := t.TempDir()
	bin := filepath.Join(dir, "kevin-relay")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, "./cmd/kevin-relay")
	build.Dir = root
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skip("cannot build kevin-relay for the image:", err, string(out))
	}

	dockerBuild := exec.CommandContext(t.Context(), "docker", "build",
		"-f", filepath.Join(root, "build", "relay.Dockerfile"), "-t", relayImageTag, dir)
	if out, err := dockerBuild.CombinedOutput(); err != nil {
		t.Skip("cannot build the relay image:", err, string(out))
	}
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

		dir := configDir(t, "plugins: echo: file: "+strconv.Quote(pkgPath)+"\n"+
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
	cfg, plugins, caps, err := LoadAndLaunch(t.Context(), dir, "", nil)
	require.NoError(t, err)
	defer CloseAll(plugins)

	r := &run{cfg: cfg, plugins: plugins, caps: caps, env: &pb.Environment{}}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = r.exportCrossScopeStep(canceledCtx, "cluster")
	require.Error(t, err, "a canceled context must fail the call")

	outputs, err := r.exportCrossScopeStep(t.Context(), "cluster")
	require.NoError(t, err, "a later call with a healthy context must not see the earlier failure")
	assert.Equal(t, output.Value{String: "from-setup"}, outputs["greeting"])
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
		dir := configDir(t, `
plugins: echo: cmd: "/nonexistent/kevin-plugin-echo"
env: a: uses: "echo:echo"
`)
		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "echo")
	})

	t.Run("starts only the plugins that steps reference", func(t *testing.T) {
		dir := configDir(t, `
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
		dir := configDir(t, `
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
		dir := configDir(t, "plugins: notecho: cmd: "+strconv.Quote(bin)+"\nenv: a: uses: \"notecho:echo\"\n")

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
// kevin.cue reaches the running proxy, and that the default-deny still
// blocks everything else.
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
		"deny defaults to true, and that default must reach the proxy")
	assert.Contains(t, string(body), "Blocked by kevin")

	cancel()
	require.NoError(t, <-done)
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

// TestGatewayPort covers loadGatewayPort/saveGatewayPort's own persistence,
// independent of startProxy - see TestStartProxyGatewayPort for how a
// pinned port interacts with the running proxy.
func TestGatewayPort(t *testing.T) {
	t.Run("round trips through save and load", func(t *testing.T) {
		workspace := t.TempDir()
		assert.Equal(t, 0, loadGatewayPort(workspace), "an empty workspace has no recorded port")

		saveGatewayPort(workspace, 54321)
		assert.Equal(t, 54321, loadGatewayPort(workspace), "loadGatewayPort must report what saveGatewayPort wrote")
	})

	t.Run("ignores unparsable content", func(t *testing.T) {
		workspace := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workspace, gatewayPortFile), []byte("not-a-port"), 0o600))

		assert.Equal(t, 0, loadGatewayPort(workspace), "unparsable content must report 0, not an error")
	})
}

// TestStartProxyGatewayPort proves that a nonzero opts.GatewayPort is used
// as-is for the gateway listener, instead of whatever loadGatewayPort would
// otherwise report, and that a pinned port already in use fails outright
// rather than silently falling back to a different one.
func TestStartProxyGatewayPort(t *testing.T) {
	t.Run("pins the requested port", func(t *testing.T) {
		requireDocker(t)

		cfg := &config.Config{Project: "kevin-gwport-pin-test", Dir: t.TempDir()}
		workspace, authority, err := prepare(t.Context(), cfg)
		require.NoError(t, err)
		network := NetworkName(cfg.Project)
		t.Cleanup(func() {
			_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
		})

		gateway, err := dockerClient.NetworkGateway(t.Context(), network)
		require.NoError(t, err)

		// Reserve a free port on the gateway address, then free it again -
		// startProxy is asked to pin exactly that port back.
		probe := bindGatewayPort(t, gateway)
		pinnedPort := mustPort(t, probe.Addr().String())
		require.NoError(t, probe.Close())

		server, err := startProxy(t.Context(), authority, proxyOptions{
			Network:     network,
			Workspace:   workspace,
			Listen:      "127.0.0.1:0",
			GatewayPort: pinnedPort,
			Domain:      "kevin.test",
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = server.Close() })

		assert.Equal(t, pinnedPort, mustPort(t, server.gatewayAddr))
		assert.Equal(t, 0, loadGatewayPort(workspace), "a pinned port must not be persisted to the auto-reuse file")
	})

	t.Run("fails when the pinned port is already in use", func(t *testing.T) {
		requireDocker(t)

		cfg := &config.Config{Project: "kevin-gwport-conflict-test", Dir: t.TempDir()}
		workspace, authority, err := prepare(t.Context(), cfg)
		require.NoError(t, err)
		network := NetworkName(cfg.Project)
		t.Cleanup(func() {
			_ = dockerClient.NetworkRemove(context.WithoutCancel(t.Context()), network)
		})

		gateway, err := dockerClient.NetworkGateway(t.Context(), network)
		require.NoError(t, err)

		held := bindGatewayPort(t, gateway)
		defer func() { _ = held.Close() }()
		heldPort := mustPort(t, held.Addr().String())

		_, err = startProxy(t.Context(), authority, proxyOptions{
			Network:     network,
			Workspace:   workspace,
			Listen:      "127.0.0.1:0",
			GatewayPort: heldPort,
			Domain:      "kevin.test",
		})
		require.Error(t, err, "a pinned port already in use must fail, not fall back silently")
	})
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
