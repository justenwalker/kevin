// Package engine creates a kevin environment and removes it again.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sync/singleflight"

	"github.com/justenwalker/kevin/internal/browser"
	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/console"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/internal/step"
)

var log = logging.New("engine")

// wantsLiveUI reports whether Run/Teardown should replace the plain
// per-event text stream with termui's live-updating step list: only when
// nothing already claims Events (a caller that supplied its own writer,
// such as a test, always gets the plain stream), stderr is actually a
// terminal, and debug logging is off - debug logging and a redrawing
// block would otherwise interleave and corrupt each other.
func wantsLiveUI(_ context.Context, opts Options) bool {
	return opts.Events == nil &&
		isatty.IsTerminal(os.Stderr.Fd()) &&
		!opts.Debug
}

// eventsWriter picks the writer that emit sends its per-event text lines
// to: the caller's own writer when it gave one, os.Stderr otherwise - or,
// when termui already owns the terminal, io.Discard, since termui's live
// block is what shows that same information instead.
func eventsWriter(opts Options, live bool) io.Writer {
	switch {
	case opts.Events != nil:
		return opts.Events
	case live:
		return io.Discard
	default:
		return os.Stderr
	}
}

// Options control one run.
type Options struct {
	// Dir is the project directory that holds the environment file.
	Dir string

	// Name selects a named environment (<Name>.kevin.<ext> or
	// .<Name>.kevin.<ext>) in Dir over the default; "" selects the
	// unnamed environment.
	Name string

	// Tags injects CUE "@tag" values (see config.Load) into a
	// package-mode environment file. A bare NAME is shorthand for
	// NAME=true. A non-empty Tags is rejected as an error at load time
	// when the resolved environment file declares no CUE package.
	Tags []string

	// VarFile is a var-file path ("KEY=VALUE" per line) supplying
	// variables: block values, or "" for none.
	VarFile string

	// Vars holds each "--var KEY=VALUE" argument, unparsed.
	Vars []string

	// Engine is the resolved container engine. The caller resolves it
	// (--engine/KEVIN_ENGINE, or auto-detection) before calling Run or
	// Teardown - the engine is a host choice, not something the
	// environment file configures.
	Engine step.Engine

	// Scope selects which DAG to run: config.ScopeSetup or config.ScopeEnv.
	Scope config.Scope

	// Keep waits for ctx like a normal run, but removes no step on exit.
	Keep bool

	// NoWait returns as soon as the steps are up, instead of blocking until
	// ctx is canceled. setup uses this: its steps persist across runs, so
	// the command must exit rather than hold the process open. NoWait
	// implies Keep - a step that returns before Down never removes anything.
	NoWait bool

	// Debug is whether the caller asked for debug-level terminal logging.
	// A live-updating step list would corrupt that interleaved output, so
	// wantsLiveUI falls back to the plain per-event text stream when it's
	// set.
	Debug bool

	// Open opens the console URL in the default browser once it's listening.
	Open bool

	// Events receives step output. The default is os.Stderr.
	Events io.Writer

	// OnEnvironment receives the environment that Run builds, once every
	// server binds and before any step runs. A caller reads the proxy
	// address, the console address, or the relay address through this hook.
	OnEnvironment func(step.Env)
}

// Run loads the environment, starts its plugins, and creates the steps of the
// selected scope. Run then blocks until ctx is canceled, and removes the steps
// in reverse order. Run removes the steps it created even when a step failed.
//
// With [Options.Keep], Run still blocks until ctx is canceled, but removes
// nothing.
func Run(ctx context.Context, opts Options) error {
	if !opts.Scope.Valid() {
		return fmt.Errorf("engine: run needs a scope, got %v", opts.Scope)
	}
	h, err := bootstrap(ctx, opts)
	if err != nil {
		return err
	}
	defer h.Close()
	cfg, rt := h.Config, h.Runtime

	// r exists from here on, and every subsequent early return past this
	// point has already created some project-labeled docker resource
	// (network, relay). committed flips true only once r.up is about to
	// run - shutdown owns cleanup from there.
	r := &run{cfg: cfg, runtime: rt, scope: opts.Scope, events: h.Events}
	committed := false
	defer earlyCleanup(ctx, r, &committed)()

	workspace, authority, err := prepare(ctx, cfg, rt)
	if err != nil {
		return err
	}
	logs, err := openSessionLogs(workspace)
	if err != nil {
		return err
	}
	defer logs.Close()

	svc, err := r.startServices(ctx, h, opts, prepared{Workspace: workspace, Authority: authority, Logs: logs})
	if err != nil {
		return err
	}
	defer func() { _ = svc.Close() }()
	store, rl := r.store, svc.relay

	env := environment(cfg, opts.Engine, opts.Scope, &liveServices{
		ProxyAddr:           svc.proxy.addr,
		ConsoleAddr:         svc.web.addr,
		Relay:               rl.Addr(),
		RelaySOCKS5Addr:     rl.SOCKS5Addr(),
		RelaySOCKS5UDPAddrs: rl.SOCKS5UDPAddrs(),
	})
	r.env = env
	r.project["http_proxy_addr"] = svc.proxy.addr
	r.project["relay"] = rl.Addr()
	notifyEnvironment(opts, env)

	if err := h.Configure(ctx, env); err != nil {
		return err
	}

	if err := r.registerScopeSteps(store); err != nil {
		return err
	}

	defer h.startUI(ctx, store)()

	// Every step's own resources are now shutdown's responsibility, called
	// unconditionally below - the deferred cleanup above must step aside.
	committed = true

	_ = r.up(ctx)
	stopWatch := r.startWatch(ctx, opts.NoWait)
	awaitDone(ctx, opts.NoWait)
	stopWatch()

	// keep leaves the environment's own resources (containers, network,
	// CA, relay) in place; this process's own proxy/console still shut
	// down normally either way.
	shutdownErr := r.shutdown(ctx, rl, opts.Keep || opts.NoWait)

	// The session's error reflects the steps still Failed when the caller
	// canceled ctx, not r.up's original upErr - a step a rerun fixed along
	// the way no longer counts against the exit code.
	return errors.Join(r.finalStepErr(), shutdownErr, r.closeForwards(), svc.Close())
}

// awaitDone waits for ctx to be canceled, unless noWait is set. Reaching
// this line usually means every step is up; a failed step keeps the
// session alive - proxy, console, everything - so its console card's
// rerun button can fix it without restarting the whole environment.
func awaitDone(ctx context.Context, noWait bool) {
	if noWait {
		return
	}
	<-ctx.Done()
}

// openBrowser opens url, and logs a failure rather than failing the run.
func openBrowser(ctx context.Context, url string) {
	if err := browser.Open(ctx, command.DefaultStarter, url); err != nil {
		log.Ctx(ctx).Warn("open browser", "error", err)
	}
}

// earlyCleanup returns Run's deferred cleanup for everything created before
// *committed goes true. reap derives what to remove from live docker state,
// so it does nothing wherever nothing was created yet, and correctly leaves
// a network or relay still shared with the other scope alone.
func earlyCleanup(ctx context.Context, r *run, committed *bool) func() {
	return func() {
		if *committed {
			return
		}
		_ = r.shutdown(context.WithoutCancel(ctx), r.relay, false)
	}
}

// shutdownTimeout bounds how long shutdown waits for steps, the relay, and
// orphans to be removed.
const shutdownTimeout = 5 * time.Minute

// shutdown removes the run's steps. keep leaves everything, including the
// relay, in place; otherwise the relay only stops if the other scope
// isn't still live and sharing it.
func (r *run) shutdown(ctx context.Context, rl trafficRelay, keep bool) error {
	// Removal needs a live context. Reaching this line usually means that the
	// user pressed Ctrl-C, which already canceled ctx. The timeout keeps a
	// hung container engine from holding the process forever.
	downCtx, cancelDown := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancelDown()
	var downErr error
	if !keep {
		downErr = r.down(downCtx)
	}

	var relayErr error
	if !keep && rl != nil {
		// rl is nil when Run's own early cleanup calls this before the
		// relay ever started. Stop the relay before reap removes the
		// network: a container still joined to the network blocks the
		// removal.
		relayErr = r.stopRelayIfAlone(downCtx, rl)
	}
	var reapErr error
	if !keep {
		reapErr = r.reap(downCtx)
	}
	return errors.Join(downErr, relayErr, reapErr)
}

// finalStepErr joins one error per step that still reports Failed when the
// session ends - what a rerun never fixed.
func (r *run) finalStepErr() error {
	var errs []error
	for _, s := range r.store.Snapshot().Steps {
		if s.State == session.Failed {
			errs = append(errs, fmt.Errorf("%s: %s", s.Name, s.Message))
		}
	}
	return errors.Join(errs...)
}

// relayStart is what startRelay needs beyond the environment file.
type relayStart struct {
	// GatewayAddr is where the proxy listens on the project network's
	// gateway.
	GatewayAddr string

	// Scope is the DAG scope that starts the relay.
	Scope config.Scope

	// Authority mints the relay's control channel certificates.
	Authority *ca.CA
}

// startRelay starts the relay.
func startRelay(ctx context.Context, rt cri.Containers, cfg *config.Config, start relayStart) (*relay.Relay, error) {
	rl, err := relay.Start(ctx, rt, relay.Options{
		Project:       cfg.Project,
		Network:       networkName(cfg.Project),
		Domain:        cfg.Domain,
		ProxyAddr:     hostGateway + ":" + portOf(start.GatewayAddr),
		Image:         relay.Ref(cfg.Relay.Image),
		Scope:         start.Scope,
		Authority:     start.Authority,
		FakeIPv4Range: cfg.Relay.Intercept.IPv4Range,
		FakeIPv6Range: cfg.Relay.Intercept.IPv6Range,
	})
	if err != nil {
		return nil, err
	}

	log.Ctx(ctx).Info("relay listening", "addr", rl.Addr())
	return rl, nil
}

// notifyEnvironment calls opts.OnEnvironment with env, when the caller set
// the hook.
func notifyEnvironment(opts Options, env step.Env) {
	if opts.OnEnvironment != nil {
		opts.OnEnvironment(env)
	}
}

// portOf returns the port of addr, or addr itself when addr carries no
// host part.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

// prepare creates the workspace, the shared network, and the authority. It
// returns the workspace path and the authority.
func prepare(ctx context.Context, cfg *config.Config, rt networkPreparer) (string, *ca.CA, error) {
	workspace := state.For(cfg.Dir, cfg.Name).Workspace()
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return "", nil, fmt.Errorf("supervisor: create %s: %w", workspace, err)
	}

	if err := rt.Available(ctx); err != nil {
		return "", nil, err
	}
	if err := rt.NetworkCreate(ctx, networkName(cfg.Project), cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: cfg.Project},
		IPv6:   true,
	}); err != nil {
		return "", nil, err
	}

	manager := ca.NewManager(cfg.Dir, cfg.Name, cfg.Project, ca.Options{})
	if _, err := manager.LoadOrGenerateRoot(); err != nil {
		return "", nil, err
	}
	authority, err := manager.LoadOrGenerateIntermediate()
	if err != nil {
		return "", nil, err
	}
	return workspace, authority, nil
}

// stepNames lists every step of the environment, in both scopes. A workload of
// one scope can still reach a workload of the other.
func stepNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Setup)+len(cfg.Env))
	for name := range cfg.Setup {
		names = append(names, name)
	}
	for name := range cfg.Env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// networkName is the docker network of one project.
func networkName(project string) string { return "kevin-" + project }

// proxyEnv is the proxy environment for a workload. NO_PROXY keeps traffic
// between two workloads, and traffic to the host, away from the proxy. Pass
// every step name: a step reaches another by that name on the docker network,
// and the proxy runs outside the network and cannot resolve it.
func proxyEnv(proxyAddr, network string, steps []string) map[string]string {
	// A workload reaches the proxy on the gateway of the network, not on the
	// loopback of the host.
	_, port, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		port = proxyAddr
	}
	endpoint := "http://" + net.JoinHostPort(hostGateway, port)

	return map[string]string{
		"HTTP_PROXY":  endpoint,
		"HTTPS_PROXY": endpoint,
		"http_proxy":  endpoint,
		"https_proxy": endpoint,
		"NO_PROXY":    noProxy(network, steps),
		"no_proxy":    noProxy(network, steps),
	}
}

// noProxy lists the destinations that a workload reaches directly.
func noProxy(network string, steps []string) string {
	direct := make([]string, 0, 7+len(steps))
	direct = append(direct,
		"localhost",
		"127.0.0.1",
		"::1",
		hostGateway,
		network,
		".svc",
		".cluster.local",
	)
	// A step joins the network under its own name. That name resolves inside
	// the network and nowhere else.
	return strings.Join(append(direct, steps...), ",")
}

// hostGateway is the name that Docker resolves to the host from inside a
// container.
const hostGateway = "host.docker.internal"

// progressTick is how often trackProgress reports an estimate to the
// console while a step runs.
const progressTick = 500 * time.Millisecond

// progressCap bounds the reported fraction below 100%: the estimate is a
// median of past runs, and a real run may still take longer.
const progressCap = 0.95

// liveServices is what a running session adds to the plugin environment.
type liveServices struct {
	ProxyAddr           string
	ConsoleAddr         string
	Relay               string
	RelaySOCKS5Addr     string
	RelaySOCKS5UDPAddrs map[int]string
}

// environment is the plugin environment for work on scope. live is nil when
// no proxy, console or relay runs: Teardown, and a command that only
// exports steps.
func environment(cfg *config.Config, engine step.Engine, scope config.Scope, live *liveServices) step.Env {
	network := networkName(cfg.Project)
	env := step.Env{
		Project:    cfg.Project,
		Workspace:  state.For(cfg.Dir, cfg.Name).Workspace(),
		Network:    network,
		CAPath:     state.ForUser().RootCert(),
		Domain:     cfg.Domain,
		ProjectDir: cfg.Dir,
		Scope:      scope,
		Engine:     engine,
	}
	if live != nil {
		env.HTTPProxyAddr = live.ProxyAddr
		env.ConsoleAddr = live.ConsoleAddr
		env.ProxyEnv = proxyEnv(live.ProxyAddr, network, stepNames(cfg))
		env.Relay = live.Relay
		env.RelaySOCKS5Addr = live.RelaySOCKS5Addr
		env.RelaySOCKS5UDPAddrs = live.RelaySOCKS5UDPAddrs
	}
	return env
}

// Teardown removes the steps of the setup scope. It runs Down for every step,
// in reverse dependency order, whether or not the step is present. A Down must
// be idempotent, thus a step that is gone already is not an error.
func Teardown(ctx context.Context, opts Options) error {
	h, err := bootstrap(ctx, opts)
	if err != nil {
		return err
	}
	defer h.Close()
	cfg, rt, workspace := h.Config, h.Runtime, h.Workspace

	steps := cfg.Steps(config.ScopeSetup)
	env := environment(cfg, opts.Engine, config.ScopeSetup, nil)
	if err = h.Configure(ctx, env); err != nil {
		return err
	}

	stepLog, err := openNDJSONLog(workspace, state.LogsFile)
	if err != nil {
		return err
	}
	defer func() { _ = stepLog.Close() }()

	r := &run{
		cfg:     cfg,
		runtime: rt,
		scope:   config.ScopeSetup,
		events:  h.Events,
		// Teardown serves no page - just a store for r's step-mutation calls
		// and, when live, the terminal UI to read.
		store:   session.NewStore(),
		env:     env,
		timings: loadTimings(ctx, filepath.Join(workspace, timingsFileName)),
		stepLog: stepLog.Logger,
	}
	// No proxy runs during Teardown, so unlike Run, there's no
	// http_proxy_addr in r.project to offer - the CA files still exist on
	// disk regardless.
	r.fill(h)
	// There is no state file for step outputs. Every step is a candidate
	// for removal, and Down carries no outputs.
	for name, step := range steps {
		r.state.Complete(map[string]output.Map{name: nil})

		// Backfills a same-scope "needs.<name>.out.*" reference for Down's
		// own rendering, the same way exportCrossScopeStep already does for
		// a cross-scope one. Leaves nil - Down still runs - for a plugin
		// with no Export, or one that never actually came up.
		ref := step.Uses
		if !stepExports(h.Caps[ref.Plugin], ref.Step) {
			continue
		}
		if exported, expErr := r.exportCrossScopeStep(ctx, name); expErr == nil {
			r.state.Complete(map[string]output.Map{name: exported.Outputs})
		}
	}

	if err := r.registerScopeSteps(r.store); err != nil {
		return err
	}

	defer h.startUI(ctx, r.store)()

	downErr := r.down(ctx)

	// Teardown is the only place a setup-owned relay is ever removed for
	// good - shutdown leaves it up while env is still live, reap always
	// skips it. Stop it before reap removes the network, same as shutdown.
	relayErr := r.closeSetupRelay(ctx, cfg)

	// reap runs last: it sweeps any orphaned container behind, then the
	// project network - but only once nothing in the env scope is still
	// live and sharing that network with this project's setup scope.
	return errors.Join(downErr, relayErr, r.reap(ctx))
}

// closeSetupRelay removes the project's relay container, if one exists and
// the env scope isn't still live and sharing it.
func (r *run) closeSetupRelay(ctx context.Context, cfg *config.Config) error {
	rl, err := relay.Lookup(ctx, r.runtime, cfg.Project, networkName(cfg.Project))
	if err != nil || rl == nil {
		return err
	}
	return r.stopRelayIfAlone(ctx, rl)
}

type run struct {
	cfg        *config.Config
	runtime    projectRuntime
	proxy      routeTable
	store      *session.Store
	scope      config.Scope
	steps      map[string]config.Step
	groups     map[string]config.Group
	plugins    map[string]stepPlugin
	caps       map[string]pluginhost.Info
	env        step.Env
	project    map[string]string
	relay      trafficRelay
	toolRoutes map[string]toolRoute
	events     io.Writer
	timings    *timingStore
	stepLog    *slog.Logger

	state runState

	// exportGroup deduplicates concurrent exportCrossScopeStep calls for
	// the same setup step name, so several consumers up at once share one
	// Export call instead of each triggering their own. Unlike a plain
	// cache, singleflight forgets a call once it completes - a failed
	// call (e.g. a canceled console/MCP rerun request) never poisons a
	// later, independent retry.
	exportGroup singleflight.Group
}

// graph builds the DAG for r's own scope, keeping only needs entries that
// name a step or group in this scope. A "setup."-prefixed cross-scope
// entry is dropped here and resolved separately by crossScopeDeps. Every
// group gets an entry too, needing all its own members - the virtual "the
// whole group is done" node, whether or not anything actually needs it.
func (r *run) graph() *dag.Graph {
	needs := make(map[string][]string, len(r.steps)+len(r.groups))
	for name, step := range r.steps {
		filtered := make([]string, 0, len(step.Needs))
		for _, dep := range step.Needs {
			if resolved, ok := r.resolveNeed(name, dep); ok {
				filtered = append(filtered, resolved)
			}
		}
		needs[name] = filtered
	}
	for name, grp := range r.groups {
		deps := make([]string, 0, len(grp.Members))
		for _, member := range grp.Members {
			deps = append(deps, config.MemberName(name, member))
		}
		needs[name] = deps
	}
	return dag.New(needs)
}

// services are the servers and relay a running session owns.
type services struct {
	proxy *proxyServer
	relay *relay.Relay
	web   *consoleServer
}

// Close stops the proxy and the console. It does not stop the relay.
func (s *services) Close() error {
	return errors.Join(s.proxy.Close(), s.web.Close())
}

// prepared is what Run sets up before it starts any server.
type prepared struct {
	Workspace string
	Authority *ca.CA
	Logs      sessionLogs
}

// startServices starts the proxy, the relay and the console, and wires them
// into r. On error it stops whatever it started.
func (r *run) startServices(ctx context.Context, h *host, opts Options, p prepared) (_ *services, err error) {
	cfg := h.Config
	network := networkName(cfg.Project)
	store := session.NewStore()

	server, err := startProxy(ctx, h.Runtime, p.Authority, proxyOptionsFor(cfg, network, recordRequest(store, p.Logs.Traffic.Logger)))
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = server.Close()
		}
	}()
	log.Ctx(ctx).Info("proxy listening", "addr", server.addr)

	rl, err := startRelay(ctx, h.Runtime, cfg, relayStart{GatewayAddr: server.gatewayAddr, Scope: opts.Scope, Authority: p.Authority})
	if err != nil {
		return nil, err
	}
	r.relay = rl

	store.SetProxyAddr(server.addr)
	r.proxy = server.proxy
	r.store = store
	r.fill(h)
	r.timings = loadTimings(ctx, filepath.Join(p.Workspace, timingsFileName))
	r.stepLog = p.Logs.Step.Logger
	tools, toolRoutes := collectTools(ctx, r.steps, h.Caps)
	r.toolRoutes = toolRoutes

	web, err := startConsole(ctx, r.newMux(cfg, p.Workspace, server.proxy, tools), cfg.Console.Listen)
	if err != nil {
		return nil, err
	}
	consoleURL := "http://" + web.addr
	log.Ctx(ctx).Info("console listening", "url", consoleURL)
	log.Ctx(ctx).Info("mcp listening", "url", consoleURL+mcpserver.Path)
	if opts.Open {
		go openBrowser(ctx, consoleURL)
	}
	return &services{proxy: server, relay: rl, web: web}, nil
}

// sessionLogs are the NDJSON logs a running session writes.
type sessionLogs struct {
	Step    ndjsonLog
	Traffic ndjsonLog
}

// openSessionLogs opens the step and traffic logs under workspace.
func openSessionLogs(workspace string) (sessionLogs, error) {
	step, err := openNDJSONLog(workspace, state.LogsFile)
	if err != nil {
		return sessionLogs{}, err
	}
	traffic, err := openNDJSONLog(workspace, trafficFileName)
	if err != nil {
		_ = step.Close()
		return sessionLogs{}, err
	}
	return sessionLogs{Step: step, Traffic: traffic}, nil
}

// Close closes both logs.
func (l sessionLogs) Close() {
	_ = l.Step.Close()
	_ = l.Traffic.Close()
}

// proxyOptionsFor builds the proxy's options from the environment file.
func proxyOptionsFor(cfg *config.Config, network string, onRecord func(proxy.Record)) proxyOptions {
	return proxyOptions{
		Network:     network,
		Listen:      cfg.Proxy.Listen,
		GatewayPort: cfg.Proxy.GatewayPort,
		Domain:      cfg.Domain,
		Allow:       cfg.Proxy.Egress.Allow,
		Deny:        cfg.Proxy.Egress.Deny,
		Passthrough: cfg.Proxy.Egress.Passthrough,
		OnRecord:    onRecord,
	}
}

// recordRequest returns the proxy's OnRecord hook: it adds each request to
// store and writes it to the traffic log.
func recordRequest(store *session.Store, traffic *slog.Logger) func(proxy.Record) {
	return func(rec proxy.Record) {
		req := session.Request{
			Time:   rec.Time,
			Method: rec.Method,
			Host:   rec.Host,
			Path:   rec.Path,
			Status: rec.Status,
			Millis: rec.Duration.Milliseconds(),
			Routed: rec.Routed,
			Denied: rec.Denied,
		}
		store.Record(req)
		traffic.Info("request",
			"request_time", req.Time, "method", req.Method, "host", req.Host, "path", req.Path,
			"status", req.Status, "millis", req.Millis,
			"routed", req.Routed, "denied", req.Denied)
	}
}

// newMux serves the console pages and the MCP endpoint.
func (r *run) newMux(cfg *config.Config, workspace string, p *proxy.Proxy, tools []mcpserver.ToolDef) *http.ServeMux {
	mcpServer := mcpserver.New(mcpserver.Options{
		Project:  cfg.Project,
		Domain:   cfg.Domain,
		LogsPath: filepath.Join(workspace, state.LogsFile),
		View:     r.store,
		Proxy:    egressView{proxy: p},
		Control:  r,
		Tools:    tools,
	})
	view := console.New(console.Config{
		Project: cfg.Project,
		Network: networkName(cfg.Project),
		Store:   r.store,
		Rerun:   r.Rerun,
	})
	mux := http.NewServeMux()
	view.RegisterRoutes(mux)
	mux.Handle(mcpserver.Path, mcpServer.Handler())
	return mux
}
