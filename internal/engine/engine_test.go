package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/relay/relaytest"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/internal/step"
)

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
func runKeep(t *testing.T, dir string, scope config.Scope, until string) (*watcher, error) {
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
		r.state.MarkStarted("a")

		assert.Equal(t, []string{"a"}, r.state.Interrupted())
	})

	t.Run("a step whose Up failed on its own is left to its plugin", func(t *testing.T) {
		r := &run{}
		r.state.MarkStarted("a")
		r.state.MarkSettled("a")

		assert.Empty(t, r.state.Interrupted())
	})

	t.Run("a completed step is not interrupted, even when a rerun started it again", func(t *testing.T) {
		r := &run{}
		r.state.MarkStarted("a")
		r.state.Complete(map[string]output.Map{"a": {"k": {String: "v"}}})
		r.state.MarkStarted("a")

		assert.Empty(t, r.state.Interrupted())
	})

	t.Run("a step that never called Up", func(t *testing.T) {
		r := &run{}
		r.state.Complete(map[string]output.Map{"a": nil})

		assert.Empty(t, r.state.Interrupted())
	})
}

// TestExportCrossScopeStepRetriesAfterFailure proves a failed
// exportCrossScopeStep call - such as one whose context a dropped
// console/MCP rerun request canceled mid-flight - never poisons a later,
// independent call for the same setup step. singleflight.Group forgets a
// call once it completes, unlike the sync.OnceValues this once used, which
// would have cached the failure for the rest of the run.
func TestExportCrossScopeStepRetriesAfterFailure(t *testing.T) {
	plugin := newMockstepPlugin(t)
	plugin.EXPECT().Export(mock.Anything, mock.Anything).RunAndReturn(
		func(ctx context.Context, _ step.ExportRequest) (step.ExportResult, error) {
			if err := ctx.Err(); err != nil {
				return step.ExportResult{}, err
			}
			return step.ExportResult{Out: output.Map{"greeting": {String: "from-setup"}}}, nil
		})
	r := &run{
		cfg:     &config.Config{Setup: map[string]config.Step{"cluster": {Uses: config.StepRef{Plugin: "echo", Step: "echo"}}}},
		plugins: map[string]stepPlugin{"echo": plugin},
		caps:    map[string]pluginhost.Info{"echo": {Steps: []pluginhost.StepInfo{{Name: "echo", Export: true}}}},
		env:     step.Env{},
	}

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := r.exportCrossScopeStep(canceledCtx, "cluster")
	require.Error(t, err, "a canceled context must fail the call")

	exported, err := r.exportCrossScopeStep(t.Context(), "cluster")
	require.NoError(t, err, "a later call with a healthy context must not see the earlier failure")
	assert.Equal(t, output.Value{String: "from-setup"}, exported.Outputs["greeting"])
}

// TestRunRejectsInvalidConfiguration covers the ways Run fails before any
// plugin runs or docker is touched: a plugin that will not start, a reserved
// namespace, an unknown plugin, a missing file, an unwritable workspace.
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

	t.Run("a run without a scope", func(t *testing.T) {
		err := Run(t.Context(), Options{})

		require.ErrorContains(t, err, "needs a scope")
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

	t.Run("rejects a step naming an undeclared plugin", func(t *testing.T) {
		dir := configDir(t, proxyBlock(t)+`
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
		dir := configDir(t, proxyBlock(t)+"env: {}\n")
		require.NoError(t, os.WriteFile(filepath.Join(dir, state.WorkspaceDir), []byte("not a directory"), 0o600))

		err := runEnv(t, dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), state.WorkspaceDir)
	})
}

// TestRunOnEvent covers what Run's real plugins never trigger through the
// black-box scenarios above: a progress event with a nonzero total. The echo
// fixture plugin always reports Total: 0.
func TestRunOnEvent(t *testing.T) {
	tests := []struct {
		name string
		ev   func(step.Events)
		want string
	}{
		{
			// A log line goes to the console and the durable log file, not
			// the terminal writer.
			name: "a log line",
			ev:   func(e step.Events) { e.Log("stdout", "hello", time.Time{}) },
			want: "",
		},
		{
			name: "progress with no total",
			ev:   func(e step.Events) { e.Progress("waiting", 0, 0) },
			want: fmt.Sprintf("%-16s %s\n", "step", "waiting"),
		},
		{
			name: "progress with a total",
			ev:   func(e step.Events) { e.Progress("copying", 3, 10) },
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
			tt.ev(r.onEvent("step"))
			assert.Equal(t, tt.want, buf.String())
		})
	}
}

func TestProxyEnvKeepsInternalTrafficOffTheProxy(t *testing.T) {
	env := proxyEnv("127.0.0.1:18080", "kevin-demo", []string{"web", "api"})

	endpoint := "http://" + hostGateway + ":18080"
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
	assert.Equal(t, "kevin-demo", networkName("demo"))
	assert.NotEqual(t, networkName("a"), networkName("b"))
}

func TestEnvironment(t *testing.T) {
	cfg := &config.Config{Project: "demo", Dir: t.TempDir(), Domain: "kevin.test"}
	live := &liveServices{ProxyAddr: "127.0.0.1:8080", ConsoleAddr: "127.0.0.1:9090", Relay: "10.0.0.2:1"}

	t.Run("a session adds only the live fields", func(t *testing.T) {
		bare := environment(cfg, step.EnginePodman, config.ScopeEnv, nil)
		full := environment(cfg, step.EnginePodman, config.ScopeEnv, live)

		assert.Empty(t, bare.HTTPProxyAddr)
		assert.Empty(t, bare.ProxyEnv)
		assert.Equal(t, "127.0.0.1:8080", full.HTTPProxyAddr)
		assert.Equal(t, "10.0.0.2:1", full.Relay)

		full.HTTPProxyAddr, full.ConsoleAddr, full.ProxyEnv, full.Relay = "", "", nil, ""
		assert.Equal(t, bare, full)
	})

	t.Run("carries the scope and engine it is given", func(t *testing.T) {
		env := environment(cfg, step.EnginePodman, config.ScopeSetup, nil)

		assert.Equal(t, config.ScopeSetup, env.Scope)
		assert.Equal(t, step.EnginePodman, env.Engine)
		assert.Equal(t, networkName("demo"), env.Network)
	})
}

// gatewayRuntime is a gatewayLookup double whose network has a fixed gateway.
type gatewayRuntime struct {
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

func TestStepContainersFor(t *testing.T) {
	backend := []step.Container{{ID: "abc", Name: "kevin-demo-backend"}}
	cluster := []step.Container{{ID: "def", Name: "kevin-demo-cluster-worker"}}

	t.Run("a same-scope need with containers contributes an entry", func(t *testing.T) {
		r := &run{}
		r.state.SetContainers("backend", backend)

		got := r.stepContainersFor(map[string]output.Map{"backend": {}}, nil)
		require.Len(t, got, 1)
		assert.Equal(t, "backend", got[0].Step)
		assert.Equal(t, backend, got[0].Containers)
	})

	t.Run("a same-scope need with no containers contributes nothing", func(t *testing.T) {
		r := &run{}
		got := r.stepContainersFor(map[string]output.Map{"waiter": {}}, nil)
		assert.Empty(t, got)
	})

	t.Run("a cross-scope need carries the setup scope", func(t *testing.T) {
		r := &run{}
		got := r.stepContainersFor(nil, map[string][]step.Container{"cluster": cluster})
		require.Len(t, got, 1)
		assert.Equal(t, config.ScopeSetup, got[0].Scope)
		assert.Equal(t, "cluster", got[0].Step)
		assert.Equal(t, cluster, got[0].Containers)
	})
}

func TestClearFaults(t *testing.T) {
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

func TestMergeStepProperty(t *testing.T) {
	ref := config.StepRef{Plugin: "echo", Step: "echo"}

	t.Run("adds a required step string property", func(t *testing.T) {
		out, err := mergeStepProperty([]byte(`{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}`), ref)
		require.NoError(t, err)

		var doc struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal(out, &doc))
		assert.Equal(t, "string", doc.Properties["step"].Type)
		assert.Equal(t, "string", doc.Properties["sql"].Type, "the plugin's own properties stay")
		assert.ElementsMatch(t, []string{"sql", "step"}, doc.Required)
	})

	t.Run("builds a schema when the plugin declares none", func(t *testing.T) {
		out, err := mergeStepProperty(nil, ref)
		require.NoError(t, err)
		var doc struct {
			Required []string `json:"required"`
		}
		require.NoError(t, json.Unmarshal(out, &doc))
		assert.Equal(t, []string{"step"}, doc.Required)
	})
}

// newUpRun builds a run whose single step "a" uses the given plugin, relay
// and route table, with no process, proxy or docker daemon behind them.
func newUpRun(t *testing.T, plugin stepPlugin, routes routeTable, rl trafficRelay) *run {
	t.Helper()
	r := &run{
		cfg:     &config.Config{},
		steps:   map[string]config.Step{"a": {Uses: config.StepRef{Plugin: "echo", Step: "echo"}}},
		plugins: map[string]stepPlugin{"echo": plugin},
		proxy:   routes,
		relay:   rl,
		env:     step.Env{},
		events:  io.Discard,
		stepLog: slog.New(slog.DiscardHandler),
		timings: &timingStore{},
	}
	r.store = session.NewStore()
	require.NoError(t, r.registerScopeSteps(r.store))
	return r
}
