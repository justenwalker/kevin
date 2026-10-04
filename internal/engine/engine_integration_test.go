//go:build integration

package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/relay/relaytest"
	"github.com/justenwalker/kevin/protos/pb"
)

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
