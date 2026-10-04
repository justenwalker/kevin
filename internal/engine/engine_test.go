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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
	"github.com/justenwalker/kevin/internal/proxy"
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

func TestExposePort(t *testing.T) {
	newEP := func(protocol string) *pb.ExposedPort {
		return &pb.ExposedPort{Name: "u", Protocol: protocol, Upstream: "not-socks5", Relay: true}
	}

	t.Run("a failed UDP forward is an error", func(t *testing.T) {
		var events bytes.Buffer
		r := &run{events: &events}
		err := r.exposePort(t.Context(), "udp", newEP("udp"), dag.Outputs{})
		require.ErrorContains(t, err, "local forward for u")
	})

	t.Run("a failed TCP forward is only a warning", func(t *testing.T) {
		var events bytes.Buffer
		r := &run{events: &events}
		require.NoError(t, r.exposePort(t.Context(), "web", newEP("tcp"), dag.Outputs{}))
		assert.Contains(t, events.String(), "warning: local forward for u")
	})

	t.Run("exposePorts marks the step failed", func(t *testing.T) {
		var events bytes.Buffer
		store := session.NewStore()
		r := &run{events: &events, store: store}
		eps := []*pb.ExposedPort{newEP("udp")}
		require.Error(t, r.exposePorts(t.Context(), "udp", eps, dag.Outputs{}))
		assert.Contains(t, events.String(), "failed: ")
	})
}
