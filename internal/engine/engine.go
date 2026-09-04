// Package engine creates a kevin environment and removes it again.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mattn/go-isatty"
	"golang.org/x/sync/singleflight"

	"github.com/justenwalker/kevin/internal/browser"
	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/console"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/httpserver"
	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/termui"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

var log = logging.New("engine")

// dockerClient runs the docker commands that the engine issues directly,
// outside any plugin. The zero value is ready to use.
var dockerClient docker.Client

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

// WorkspaceDir is the state directory of one project, relative to the project
// directory.
const WorkspaceDir = ".kevin"

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

	// Scope selects which DAG to run: config.ScopeSetup or config.ScopeEnv.
	Scope string

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
	OnEnvironment func(*pb.Environment)
}

// Run loads the environment, starts its plugins, and creates the steps of the
// selected scope. Run then blocks until ctx is canceled, and removes the steps
// in reverse order. Run removes the steps it created even when a step failed.
//
// With [Options.Keep], Run still blocks until ctx is canceled, but removes
// nothing.
func Run(ctx context.Context, opts Options) error {
	live := wantsLiveUI(ctx, opts)
	opts.Events = eventsWriter(opts, live)

	cfg, plugins, caps, err := LoadAndLaunch(ctx, opts.Dir, opts.Name, opts.Tags)
	defer CloseAll(plugins)
	if err != nil {
		return err
	}

	workspace, authority, err := prepare(ctx, cfg)
	if err != nil {
		return err
	}
	timings := loadTimings(ctx, filepath.Join(workspace, TimingsFile))

	stepLog, err := openNDJSONLog(workspace, LogsFile)
	if err != nil {
		return err
	}
	defer func() { _ = stepLog.Close() }()

	traffic, err := openNDJSONLog(workspace, TrafficFile)
	if err != nil {
		return err
	}
	defer func() { _ = traffic.Close() }()

	network := NetworkName(cfg.Project)

	server, err := startProxy(ctx, authority, proxyOptions{
		Network:     network,
		Listen:      cfg.Proxy.Listen,
		GatewayPort: cfg.Proxy.GatewayPort,
		Domain:      cfg.Domain,
		Allow:       cfg.Proxy.Egress.Allow,
		Deny:        cfg.Proxy.Egress.Deny,
	})
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()

	log.Ctx(ctx).Info("proxy listening", "addr", server.addr)

	rl, err := startRelay(ctx, cfg, network, server.gatewayAddr, opts.Scope)
	if err != nil {
		return err
	}
	// shutdown, below, decides whether the relay actually closes - keep or
	// a still-live other scope leaves it running for a later process to
	// reuse. An unconditional defer here would override that decision on
	// every exit path, including the ones shutdown gets right.

	store := session.NewStore()
	store.SetProxyAddr(server.addr)
	server.proxy.OnRecord(func(rec proxy.Record) {
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
	})

	r := &run{
		cfg:     cfg,
		proxy:   server.proxy,
		store:   store,
		scope:   opts.Scope,
		steps:   cfg.Steps(opts.Scope),
		groups:  cfg.Groups(opts.Scope),
		plugins: plugins,
		caps:    caps,
		events:  opts.Events,
		timings: timings,
		stepLog: stepLog.Logger,
	}
	tools, toolRoutes := collectTools(ctx, r.steps, caps)
	r.toolRoutes = toolRoutes
	mcpServer := mcpserver.New(cfg.Project, cfg.Domain, store, server.proxy, r.RerunStep, r.exportStep, tools, r.callTool)
	view := console.New(console.Config{
		Project: cfg.Project,
		Network: network,
		Store:   store,
		Rerun:   r.RerunStep,
	})

	mux := http.NewServeMux()
	view.RegisterRoutes(mux)
	mux.Handle(mcpserver.Path, mcpServer.Handler())

	web, err := startConsole(ctx, mux, cfg.Console.Listen)
	if err != nil {
		return err
	}
	defer func() { _ = web.Close() }()

	consoleURL := "http://" + web.addr
	log.Ctx(ctx).Info("console listening", "url", consoleURL)
	log.Ctx(ctx).Info("mcp listening", "url", consoleURL+mcpserver.Path)
	if opts.Open {
		go func() {
			if openErr := browser.Open(ctx, consoleURL); openErr != nil {
				log.Ctx(ctx).Warn("open browser", "error", openErr)
			}
		}()
	}

	env := &pb.Environment{
		Project:         cfg.Project,
		Workspace:       workspace,
		Network:         network,
		CaPath:          ca.RootCertPath(),
		HttpProxyAddr:   server.addr,
		ConsoleAddr:     web.addr,
		ProxyEnv:        ProxyEnv(server.addr, network, stepNames(cfg)),
		Domain:          cfg.Domain,
		Relay:           rl.Addr(),
		RelaySocks5Addr: rl.SOCKS5Addr(),
		ProjectDir:      cfg.Dir,
		Scope:           opts.Scope,
	}
	r.env = env
	r.project = ca.ProjectVars(cfg.Dir, cfg.Name)
	r.project["http_proxy_addr"] = server.addr
	notifyEnvironment(opts, env)

	if err := ConfigureAll(ctx, cfg.Plugins, plugins, env); err != nil {
		return err
	}

	if err := r.registerScopeSteps(store); err != nil {
		return err
	}

	if live {
		stop := termui.New(os.Stderr).Start(ctx, store)
		defer stop()
	}

	_ = r.up(ctx)
	awaitDone(ctx, opts.NoWait)

	// keep leaves the environment's own resources (containers, network,
	// CA, relay) in place; this process's own proxy/console still shut
	// down normally either way.
	shutdownErr := r.shutdown(ctx, rl, opts.Keep || opts.NoWait)

	// The session's error reflects the steps still Failed when the caller
	// canceled ctx, not r.up's original upErr - a step a rerun fixed along
	// the way no longer counts against the exit code.
	return errors.Join(r.finalStepErr(), shutdownErr, r.closeForwards(), server.Close(), web.Close())
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

// shutdown removes the run's steps. keep leaves everything, including the
// relay, in place; otherwise the relay only stops if the other scope
// isn't still live and sharing it.
func (r *run) shutdown(ctx context.Context, rl *relay.Relay, keep bool) error {
	// Removal needs a live context. Reaching this line usually means that the
	// user pressed Ctrl-C, which already canceled ctx.
	downCtx := context.WithoutCancel(ctx)
	var downErr error
	if !keep {
		downErr = r.down(downCtx)
	}

	var relayErr error
	if !keep {
		// Stop the relay before reap removes the network: a container still
		// joined to the network blocks the removal.
		otherLive, liveErr := r.otherScopeLive(downCtx)
		if liveErr != nil {
			relayErr = liveErr
		} else if len(otherLive) == 0 {
			relayErr = rl.Close()
		}
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

// startRelay starts the relay.
func startRelay(ctx context.Context, cfg *config.Config, network, gatewayAddr, scope string) (*relay.Relay, error) {
	rl, err := relay.Start(ctx, relay.Options{
		Project:   cfg.Project,
		Network:   network,
		Domain:    cfg.Domain,
		ProxyAddr: HostGateway + ":" + portOf(gatewayAddr),
		Image:     relay.Ref(cfg.Relay.Image),
		Scope:     scope,
	})
	if err != nil {
		return nil, err
	}

	log.Ctx(ctx).Info("relay listening", "addr", rl.Addr())
	return rl, nil
}

// notifyEnvironment calls opts.OnEnvironment with env, when the caller set
// the hook.
func notifyEnvironment(opts Options, env *pb.Environment) {
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

// consoleServer is a running console and the means to stop it.
type consoleServer struct {
	addr string

	stop context.CancelFunc
	done <-chan error
	once sync.Once
	err  error
}

// startConsole binds the listener and serves handler on it.
func startConsole(ctx context.Context, handler http.Handler, listen string) (*consoleServer, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("supervisor: listen on %s: %w", listen, err)
	}

	serveCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- httpserver.Serve(serveCtx, ln, handler) }()

	return &consoleServer{addr: ln.Addr().String(), stop: stop, done: done}, nil
}

// Close stops the console. Close is idempotent.
func (s *consoleServer) Close() error {
	s.once.Do(func() {
		s.stop()
		s.err = <-s.done
	})
	return s.err
}

// prepare creates the workspace, the shared network, and the authority. It
// returns the workspace path and the authority.
func prepare(ctx context.Context, cfg *config.Config) (string, *ca.CA, error) {
	workspace := filepath.Join(cfg.Dir, WorkspaceDir, cfg.Name)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return "", nil, fmt.Errorf("supervisor: create %s: %w", workspace, err)
	}

	if err := dockerClient.Available(ctx); err != nil {
		return "", nil, err
	}
	if err := dockerClient.NetworkCreate(ctx, NetworkName(cfg.Project), map[string]string{
		cri.LabelProject: cfg.Project,
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

// proxyServer is a running proxy and the means to stop it.
type proxyServer struct {
	proxy *proxy.Proxy
	addr  string

	// gatewayAddr is where the proxy listens on the docker bridge gateway.
	// The relay reaches the proxy there, from inside the shared network.
	gatewayAddr string

	stop context.CancelFunc
	done <-chan error
	once sync.Once
	err  error
}

// proxyOptions configures [startProxy].
type proxyOptions struct {
	// Network is the shared docker network. The gateway listener binds its
	// gateway address.
	Network string

	// Listen is the address of the primary, host-facing listener.
	Listen string

	// GatewayPort is the gateway listener's port, used as-is with no
	// fallback - a bind failure is a hard error.
	GatewayPort int

	Domain string
	Allow  []string
	Deny   bool
}

// startProxy binds the listener and serves on it. The listener binds before
// the DAG runs, because kind copies the proxy address into its node containers
// when it creates them, and the address must not change afterwards.
//
// startProxy also binds a second listener on the gateway address of
// opts.Network, when the host can bind that address. A container on that
// network reaches the proxy there. The proxy never binds 0.0.0.0.
func startProxy(ctx context.Context, authority *ca.CA, opts proxyOptions) (*proxyServer, error) {
	p, err := proxy.New(authority, opts.Domain, opts.Allow, opts.Deny)
	if err != nil {
		return nil, err
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("supervisor: listen on %s: %w", opts.Listen, err)
	}

	gateway, err := dockerClient.NetworkGateway(ctx, opts.Network)
	if err != nil {
		return nil, err
	}

	listeners := []net.Listener{ln}
	gatewayAddr := ln.Addr().String()
	gatewayLn, err := lc.Listen(ctx, "tcp", net.JoinHostPort(gateway.String(), strconv.Itoa(opts.GatewayPort)))
	switch {
	case err == nil:
		listeners = append(listeners, gatewayLn)
		gatewayAddr = gatewayLn.Addr().String()
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		// Docker Desktop on macOS and Windows runs the daemon inside a VM.
		// The gateway address exists only inside that VM, and the host
		// cannot bind it there. host.docker.internal already reaches a
		// listener on the host loopback, so the primary listener covers the
		// relay too.
	default:
		return nil, fmt.Errorf("supervisor: listen on %s: %w", net.JoinHostPort(gateway.String(), strconv.Itoa(opts.GatewayPort)), err)
	}

	// The proxy must outlive an interrupt, because removal of a step can still
	// need it.
	serveCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- p.Serve(serveCtx, listeners...) }()

	return &proxyServer{
		proxy:       p,
		addr:        ln.Addr().String(),
		gatewayAddr: gatewayAddr,
		stop:        stop,
		done:        done,
	}, nil
}

// Close stops the proxy and reports how serving ended. Close is idempotent.
func (s *proxyServer) Close() error {
	s.once.Do(func() {
		s.stop()
		s.err = <-s.done
	})
	return s.err
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

// NetworkName is the docker network of one project.
func NetworkName(project string) string { return "kevin-" + project }

// ProxyEnv is the proxy environment for a workload. NO_PROXY keeps traffic
// between two workloads, and traffic to the host, away from the proxy. Pass
// every step name: a step reaches another by that name on the docker network,
// and the proxy runs outside the network and cannot resolve it.
func ProxyEnv(proxyAddr, network string, steps []string) map[string]string {
	// A workload reaches the proxy on the gateway of the network, not on the
	// loopback of the host.
	_, port, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		port = proxyAddr
	}
	endpoint := "http://" + net.JoinHostPort(HostGateway, port)

	return map[string]string{
		"HTTP_PROXY":  endpoint,
		"HTTPS_PROXY": endpoint,
		"http_proxy":  endpoint,
		"https_proxy": endpoint,
		"NO_PROXY":    NoProxy(network, steps),
		"no_proxy":    NoProxy(network, steps),
	}
}

// NoProxy lists the destinations that a workload reaches directly.
func NoProxy(network string, steps []string) string {
	direct := make([]string, 0, 7+len(steps))
	direct = append(direct,
		"localhost",
		"127.0.0.1",
		"::1",
		HostGateway,
		network,
		".svc",
		".cluster.local",
	)
	// A step joins the network under its own name. That name resolves inside
	// the network and nowhere else.
	return strings.Join(append(direct, steps...), ",")
}

// HostGateway is the name that Docker resolves to the host from inside a
// container.
const HostGateway = "host.docker.internal"

// PluginPkgDir holds the extracted contents of every file: source plugin,
// one subdirectory per plugin name, inside the workspace.
const PluginPkgDir = "plugins"

// progressTick is how often trackProgress reports an estimate to the
// console while a step runs.
const progressTick = 500 * time.Millisecond

// progressCap bounds the reported fraction below 100%: the estimate is a
// median of past runs, and a real run may still take longer.
const progressCap = 0.95

// Teardown removes the steps of the setup scope. It runs Down for every step,
// in reverse dependency order, whether or not the step is present. A Down must
// be idempotent, thus a step that is gone already is not an error.
func Teardown(ctx context.Context, opts Options) error {
	live := wantsLiveUI(ctx, opts)
	opts.Events = eventsWriter(opts, live)

	cfg, plugins, caps, err := LoadAndLaunch(ctx, opts.Dir, opts.Name, opts.Tags)
	defer CloseAll(plugins)
	if err != nil {
		return err
	}

	steps := cfg.Steps(config.ScopeSetup)
	workspace := filepath.Join(cfg.Dir, WorkspaceDir, cfg.Name)
	env := &pb.Environment{
		Project:   cfg.Project,
		Workspace: workspace,
		Network:   NetworkName(cfg.Project),
		Scope:     config.ScopeSetup,
	}
	if err = ConfigureAll(ctx, cfg.Plugins, plugins, env); err != nil {
		return err
	}

	stepLog, err := openNDJSONLog(workspace, LogsFile)
	if err != nil {
		return err
	}
	defer func() { _ = stepLog.Close() }()

	r := &run{
		cfg:     cfg,
		scope:   config.ScopeSetup,
		steps:   steps,
		groups:  cfg.Groups(config.ScopeSetup),
		plugins: plugins,
		caps:    caps,
		events:  opts.Events,
		// Teardown serves no page - just a store for r's step-mutation calls
		// and, when live, the terminal UI to read.
		store: session.NewStore(),
		env:   env,
		// No proxy runs during Teardown, so unlike Run, there's no
		// http_proxy_addr to offer - the CA files still exist on disk
		// regardless.
		project: ca.ProjectVars(cfg.Dir, cfg.Name),
		timings: loadTimings(ctx, filepath.Join(workspace, TimingsFile)),
		stepLog: stepLog.Logger,
		// There is no state file for step outputs. Every step is a candidate
		// for removal, and Down carries no outputs.
		completed: make(map[string]dag.Outputs, len(steps)),
	}
	for name, step := range steps {
		r.completed[name] = nil

		// Backfills a same-scope "needs.<name>.out.*" reference for Down's
		// own rendering, the same way exportCrossScopeStep already does for
		// a cross-scope one. Leaves nil - Down still runs - for a plugin
		// with no Export, or one that never actually came up.
		ref, refErr := config.ParseStepRef(step.Uses)
		if refErr != nil {
			return refErr
		}
		if !stepExports(caps[ref.Plugin], ref.Step) {
			continue
		}
		if outputs, expErr := r.exportCrossScopeStep(ctx, name); expErr == nil {
			r.completed[name] = outputs
		}
	}

	if err := r.registerScopeSteps(r.store); err != nil {
		return err
	}

	if live {
		stop := termui.New(os.Stderr).Start(ctx, r.store)
		defer stop()
	}

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
	otherLive, err := r.otherScopeLive(ctx)
	if err != nil || len(otherLive) > 0 {
		return err
	}
	rl, err := relay.Lookup(ctx, cfg.Project, NetworkName(cfg.Project))
	if err != nil || rl == nil {
		return err
	}
	return rl.Close()
}

// ConfigureAll sends the config block of every plugin, once before any step
// of that plugin runs. A plugin with no config block still receives the
// call, with an empty config.
func ConfigureAll(ctx context.Context, specs map[string]config.PluginSpec, clients map[string]*pluginhost.Client, env *pb.Environment) error {
	for name, client := range clients {
		if err := client.Configure(ctx, specs[name].Config, env); err != nil {
			return err
		}
	}
	return nil
}

// stepKindLabel returns the console-facing label for k, or "" for an
// unspecified kind.
// stepKind returns the Kind that info's step type name reports, or
// STEP_KIND_UNSPECIFIED when info offers no such step.
func stepKind(info pluginhost.Info, name string) pb.StepKind {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Kind
		}
	}
	return pb.StepKind_STEP_KIND_UNSPECIFIED
}

// stepIdempotent reports whether info's step type name is safe to call Up on
// again.
func stepIdempotent(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Idempotent
		}
	}
	return false
}

// stepImplementsDown reports whether info's step type name implements Down.
func stepImplementsDown(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Down
		}
	}
	return false
}

func stepKindLabel(k pb.StepKind) string {
	switch k {
	case pb.StepKind_STEP_KIND_UNSPECIFIED:
		return ""
	case pb.StepKind_STEP_KIND_RESOURCE:
		return "resource"
	case pb.StepKind_STEP_KIND_ACTION:
		return "action"
	case pb.StepKind_STEP_KIND_PROBE:
		return "probe"
	default:
		return ""
	}
}

// isCompactStep reports whether the console should render this step as a
// single muted line instead of a full card: a probe by definition, plus
// builtin:route, which only registers a proxy mapping and creates nothing
// of its own despite being kind action. Both are gates for some other
// resource/action, not something worth equal visual weight.
func isCompactStep(kind, plugin, step string) bool {
	return kind == "probe" || (plugin == "builtin" && step == "route")
}

// collectCaps asks every plugin for its Info: the configuration schema, the
// schema and capabilities of each step type it offers.
func collectCaps(ctx context.Context, plugins map[string]*pluginhost.Client) (map[string]pluginhost.Info, error) {
	caps := make(map[string]pluginhost.Info, len(plugins))
	for name, client := range plugins {
		info, err := client.Info(ctx)
		if err != nil {
			return nil, err
		}
		caps[name] = info
		log.Ctx(ctx).Debug("plugin ready", "plugin", name, "version", info.Version)
	}
	return caps, nil
}

// run holds the state of one scope.
// toolRoute maps a namespaced MCP tool name back to the plugin, step
// type, and bare tool name a plugin declared it under.
type toolRoute struct {
	plugin string
	step   string
	tool   string
}

// collectTools builds the MCP tool list for every distinct step type
// referenced in steps that declares tools, namespaced
// "<plugin>_<type>_<tool>". Returns the tool list alongside the route
// each namespaced name resolves to.
func collectTools(ctx context.Context, steps map[string]config.Step, caps map[string]pluginhost.Info) ([]mcpserver.ToolDef, map[string]toolRoute) {
	routes := make(map[string]toolRoute)
	var defs []mcpserver.ToolDef
	for _, step := range steps {
		ref, refErr := config.ParseStepRef(step.Uses)
		if refErr != nil {
			continue
		}
		for _, st := range caps[ref.Plugin].Steps {
			if st.Name != ref.Step {
				continue
			}
			for _, t := range st.Tools {
				name := ref.Plugin + "_" + ref.Step + "_" + t.Name
				if _, ok := routes[name]; ok {
					continue
				}
				schema, mergeErr := mergeStepProperty(t.InputSchema, ref)
				if mergeErr != nil {
					log.Ctx(ctx).Warn("skipping mcp tool with an invalid input schema", "tool", name, "error", mergeErr)
					continue
				}
				routes[name] = toolRoute{plugin: ref.Plugin, step: ref.Step, tool: t.Name}
				defs = append(defs, mcpserver.ToolDef{Name: name, Description: t.Description, InputSchema: schema})
			}
		}
	}
	return defs, routes
}

// mergeStepProperty adds a required "step" string property to schema, a
// plugin-declared "object" JSON Schema document, naming the step
// instance a call to this tool targets.
func mergeStepProperty(schema []byte, ref config.StepRef) ([]byte, error) {
	doc := map[string]any{"type": "object"}
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", ref, err)
		}
	}
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	props["step"] = map[string]any{
		"type":        "string",
		"description": fmt.Sprintf("the name of a %s step, from kevin.cue", ref),
	}
	doc["properties"] = props

	required, _ := doc["required"].([]any)
	doc["required"] = append(required, "step")

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	return out, nil
}

type run struct {
	cfg        *config.Config
	proxy      *proxy.Proxy
	store      *session.Store
	scope      string
	steps      map[string]config.Step
	groups     map[string]config.Group
	plugins    map[string]*pluginhost.Client
	caps       map[string]pluginhost.Info
	env        *pb.Environment
	project    map[string]string
	toolRoutes map[string]toolRoute
	events     io.Writer
	timings    *timingStore
	stepLog    *slog.Logger

	forwardsMu sync.Mutex
	forwards   []*portForward

	// systemOutputs maps a step name to kevin-computed values (currently
	// expose_<name>/forward_<name> for an ExposedPort) - kept separate from
	// a step's own plugin-authored outputs so it can never collide with
	// one, and read into the "system" CEL variable.
	systemMu      sync.Mutex
	systemOutputs map[string]dag.Outputs

	// completed maps a step name to the outputs that the step published.
	// completedMu guards it: r.up and a rerun triggered from the console
	// can run concurrently once a step reaches Ready or Failed.
	completedMu sync.Mutex
	completed   map[string]dag.Outputs

	// exportGroup deduplicates concurrent exportCrossScopeStep calls for
	// the same setup step name, so several consumers up at once share one
	// Export call instead of each triggering their own. Unlike a plain
	// cache, singleflight forgets a call once it completes - a failed
	// call (e.g. a canceled console/MCP rerun request) never poisons a
	// later, independent retry.
	exportGroup singleflight.Group

	// stepLocksMu guards stepLocks, one per-step mutex per step name.
	// upStep holds a step's lock for the length of its call and rejects
	// with ErrStepBusy rather than wait when it's already held, so a
	// rerun requested for a step still Pending in the initial r.up walk -
	// or already mid-rerun - can never run Up twice at once for it.
	stepLocksMu sync.Mutex
	stepLocks   map[string]*sync.Mutex
}

// stepLock returns the mutex serializing upStep calls for name, creating it
// on first use.
func (r *run) stepLock(name string) *sync.Mutex {
	r.stepLocksMu.Lock()
	defer r.stepLocksMu.Unlock()
	if r.stepLocks == nil {
		r.stepLocks = map[string]*sync.Mutex{}
	}
	mu, ok := r.stepLocks[name]
	if !ok {
		mu = &sync.Mutex{}
		r.stepLocks[name] = mu
	}
	return mu
}

// snapshotCompleted returns a copy of r.completed, safe to read without
// holding completedMu.
func (r *run) snapshotCompleted() map[string]dag.Outputs {
	r.completedMu.Lock()
	defer r.completedMu.Unlock()
	out := make(map[string]dag.Outputs, len(r.completed))
	maps.Copy(out, r.completed)
	return out
}

// mergeCompleted records every step in results as complete.
func (r *run) mergeCompleted(results map[string]dag.Outputs) {
	r.completedMu.Lock()
	defer r.completedMu.Unlock()
	if r.completed == nil {
		r.completed = make(map[string]dag.Outputs, len(results))
	}
	maps.Copy(r.completed, results)
}

// addForward records pf for closeForwards to close at session teardown.
// Concurrent DAG steps call this from separate goroutines during r.up.
func (r *run) addForward(pf *portForward) {
	r.forwardsMu.Lock()
	r.forwards = append(r.forwards, pf)
	r.forwardsMu.Unlock()
}

// closeForwards closes every forward listener r.up opened.
func (r *run) closeForwards() error {
	r.forwardsMu.Lock()
	forwards := r.forwards
	r.forwardsMu.Unlock()
	errs := make([]error, len(forwards))
	for i, pf := range forwards {
		errs[i] = pf.Close()
	}
	return errors.Join(errs...)
}

// setupPrefix marks a needs entry as naming a setup-scope step instead of
// one in the running scope. Only meaningful on an env-scope needs entry.
const setupPrefix = "setup."

// validateNeeds checks every needs entry of both of cfg's scopes. Called
// once from LoadAndLaunch, before graph() or docker is touched. A
// same-scope name is left for dag.Validate to check later. An env step's
// needs may additionally name a step prefixed "setup.", resolved in the
// setup scope via Export instead of Up; a setup step accepts no such
// prefix.
func validateNeeds(cfg *config.Config) error {
	for _, scope := range []string{config.ScopeSetup, config.ScopeEnv} {
		steps := cfg.Steps(scope)
		names := make([]string, 0, len(steps))
		for name := range steps {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			for _, dep := range steps[name].Needs {
				if err := validateNeedsEntry(cfg, scope, name, dep); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateNeedsEntry checks that dep, one needs entry of name in scope,
// resolves to a real step or group - same-scope, or (only for an env
// step) a "setup.<name>" cross-scope entry.
func validateNeedsEntry(cfg *config.Config, scope, name, dep string) error {
	if setupName, ok := strings.CutPrefix(dep, setupPrefix); ok {
		if scope != config.ScopeEnv {
			return fmt.Errorf("%s: needs %q: only an env step can use a %q dependency", name, dep, setupPrefix)
		}
		if _, ok := cfg.Setup[setupName]; ok {
			return nil
		}
		if _, ok := cfg.Groups(config.ScopeSetup)[setupName]; ok {
			return nil
		}
		return fmt.Errorf("%s: needs %q: no such step in scope %q", name, dep, config.ScopeSetup)
	}
	if group, _, ok := strings.Cut(name, groupSep); ok && slices.Contains(cfg.Groups(scope)[group].Members, dep) {
		return nil
	}
	if _, ok := cfg.Steps(scope)[dep]; ok {
		return nil
	}
	if _, ok := cfg.Groups(scope)[dep]; ok {
		return nil
	}
	return fmt.Errorf("%s: needs %q: no such step in scope %q", name, dep, scope)
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
			deps = append(deps, memberName(name, member))
		}
		needs[name] = deps
	}
	return dag.New(needs)
}

// trackProgress starts a ticker that reports an estimated completion
// fraction for step name to the console, based on estimate. estimate <= 0
// means no history exists for this step; trackProgress then does nothing,
// and the console keeps showing the plain state pill with no bar. The
// returned stop func must be called exactly once, when the step's plugin
// call returns; it also stops the ticker if ctx is canceled first.
func (r *run) trackProgress(ctx context.Context, name string, estimate time.Duration) func() {
	if estimate <= 0 {
		return func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	start := time.Now()
	go func() {
		ticker := time.NewTicker(progressTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fraction := float64(time.Since(start)) / float64(estimate)
				if fraction > progressCap {
					fraction = progressCap
				}
				r.store.SetStepProgress(name, fraction)
			}
		}
	}()
	return cancel
}

// renderWith resolves name's `with` block against deps and the recorded
// system outputs of those deps, plus setupDeps for any cross-scope
// "setup.<name>" needs entry, for either an Up or a Down request.
func (r *run) renderWith(name string, step config.Step, deps, setupDeps map[string]dag.Outputs) (json.RawMessage, error) {
	r.systemMu.Lock()
	system := make(map[string]dag.Outputs, len(deps))
	for dep := range deps {
		if so, ok := r.systemOutputs[dep]; ok {
			system[dep] = so
		}
	}
	r.systemMu.Unlock()

	// A group member's own needs/with block names a sibling by that
	// sibling's bare name (graph() joins the DAG edge to its
	// "<group>.<sibling>" node internally) - expose it under that bare
	// name here too, so needs.<sibling>... resolves the way the member
	// actually wrote it.
	deps = localizeDeps(name, deps)
	system = localizeDeps(name, system)

	with, err := expr.Render(step.With, name, expr.Scopes{Needs: deps, System: system, Setup: setupDeps, Project: r.project})
	if err != nil {
		return nil, fmt.Errorf("%s: with: %w", name, err)
	}
	return with, nil
}

// reportUpFailure marks name Failed and logs err, unless ctx is already
// canceled - then err is just this step's in-flight call reacting to the
// same shutdown that's tearing down the whole run, not a genuine failure.
// markSkipped gives an unreported step this treatment its final state.
func (r *run) reportUpFailure(ctx context.Context, name string, err error) {
	if ctx.Err() != nil {
		return
	}
	msg := uerr.Display(err)
	r.emit(name, "failed: "+msg)
	r.store.SetStep(name, session.Failed, msg)
}

// upStep calls Up on one step. It is the dag.NodeFunc both the initial
// bring-up and a console-triggered rerun run through, so a rerun gets route
// registration, egress allow, detail/progress reporting, and timing history
// exactly like the first attempt. On success it also records its own
// output into r.completed directly, rather than leaving that to the
// walk's aggregate return value.
func (r *run) upStep(ctx context.Context, name string, deps map[string]dag.Outputs) (dag.Outputs, error) {
	if grp, ok := r.groups[name]; ok {
		out, err := r.upGroup(ctx, name, grp, deps)
		if err == nil {
			// Record as soon as this step succeeds: up's Walk and
			// RerunStep's WalkFrom run every step in one goroutine group
			// with no ordering guarantee between them, so a slow step
			// still in flight elsewhere would otherwise leave this one's
			// output unrecorded for as long as that other step runs - a
			// rerun requested on anything else in that window would see
			// no prior output for a dependency that is, in fact, long
			// since Ready.
			r.mergeCompleted(map[string]dag.Outputs{name: out})
		}
		return out, err
	}

	mu := r.stepLock(name)
	if !mu.TryLock() {
		return nil, fmt.Errorf("%s: %w", name, session.ErrStepBusy)
	}
	defer mu.Unlock()

	step := r.steps[name]
	ref, refErr := config.ParseStepRef(step.Uses)
	if refErr != nil {
		r.reportUpFailure(ctx, name, refErr)
		return nil, refErr
	}
	client := r.plugins[ref.Plugin]

	setupDeps, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		r.reportUpFailure(ctx, name, err)
		return nil, err
	}

	with, err := r.renderWith(name, step, deps, setupDeps)
	if err != nil {
		r.reportUpFailure(ctx, name, err)
		return nil, err
	}

	req := &pb.UpRequest{
		Step:   name,
		Type:   ref.Step,
		Env:    r.env,
		Config: with,
		Deps:   depsToProto(depsWithSetup(deps, setupDeps)),
	}

	estimate, _ := r.timings.EstimateUp(name, ref.String())
	start := time.Now()
	stop := r.trackProgress(ctx, name, estimate)
	defer stop()

	r.emit(name, "up")
	r.store.SetStep(name, session.Running, "")
	// A rerun's Up republishes whatever detail rows are still true; clear
	// the old ones first so a rerun doesn't pile a second copy onto them.
	r.store.ClearStepDetails(name)

	result, upErr := client.Up(ctx, req, r.onEvent(name))
	if upErr != nil {
		r.reportUpFailure(ctx, name, upErr)
		return nil, upErr
	}

	for _, route := range result.GetRoutes() {
		r.proxy.AddRoutes(proxy.Route{
			Host:     route.GetHost(),
			Upstream: route.GetUpstream(),
			TLS:      route.GetTls(),
		})
		r.emit(name, "serving https://"+route.GetHost())
	}
	systemThis := dag.Outputs{}
	for _, ep := range result.GetExposedPorts() {
		r.emit(name, fmt.Sprintf("exposing %s %s at %s", ep.GetProtocol(), ep.GetName(), ep.GetUpstream()))
		systemThis["expose_"+ep.GetName()] = output.Value{String: ep.GetUpstream()}

		if ep.GetProtocol() == "socks5" {
			pf, fwdErr := newPortForward(ctx, ep)
			if fwdErr != nil {
				r.emit(name, "warning: local forward for "+ep.GetName()+": "+fwdErr.Error())
				continue
			}
			r.addForward(pf)
			addr := pf.Addr().String()
			r.emit(name, fmt.Sprintf("forwarding %s at %s", ep.GetName(), addr))
			r.store.AddStepDetail(name, session.Detail{
				Label: ep.GetName() + " (local)", Value: addr, Copyable: true,
			})
			systemThis["forward_"+ep.GetName()] = output.Value{String: addr}
		}
	}
	if len(systemThis) > 0 {
		r.systemMu.Lock()
		if r.systemOutputs == nil {
			r.systemOutputs = make(map[string]dag.Outputs)
		}
		r.systemOutputs[name] = systemThis
		r.systemMu.Unlock()
	}
	r.proxy.AllowEgress(result.GetEgressAllow()...)

	for _, d := range result.GetDetails() {
		r.emit(name, "detail: "+d.GetLabel())
		r.store.AddStepDetail(name, session.Detail{
			Label:     d.GetLabel(),
			Value:     d.GetValue().GetStringValue(),
			Copyable:  d.GetCopyable(),
			Href:      d.GetHref(),
			Sensitive: d.GetValue().GetSensitive(),
		})
	}

	r.emit(name, "ready")
	r.store.SetStep(name, session.Ready, "")
	r.timings.RecordUp(ctx, name, ref.String(), time.Since(start))

	out := outputsFromProto(result.GetOutputs())
	r.mergeCompleted(map[string]dag.Outputs{name: out})
	return out, nil
}

func (r *run) up(ctx context.Context) error {
	results, err := r.graph().Walk(ctx, r.upStep)
	r.markSkipped(r.graph().Steps(), results)
	return err
}

// RerunStep re-executes step name. With cascade false, only name runs -
// every other step keeps its recorded output. With cascade true, name's
// transitive dependents are re-run too: a dependent that never completed
// always joins (it was skipped, nothing of its own to protect); a
// dependent that already completed joins only when its step type is
// idempotent. name itself always runs, regardless of its own idempotent
// flag - it was targeted directly.
func (r *run) RerunStep(ctx context.Context, name string, cascade bool) error {
	completed := r.snapshotCompleted()

	toRun := map[string]bool{name: true}
	if cascade {
		for _, dep := range r.graph().Dependents(name) {
			if _, ok := completed[dep]; !ok {
				toRun[dep] = true
				continue
			}
			if r.idempotent(dep) {
				toRun[dep] = true
			}
		}
	}

	names := make([]string, 0, len(toRun))
	for step := range toRun {
		names = append(names, step)
	}

	results, err := r.graph().WalkFrom(ctx, toRun, completed, r.upStep)
	r.markSkipped(names, results)
	return err
}

// exportStep asks name's plugin how to reach what it created, against this
// session's already-running plugin client.
func (r *run) exportStep(ctx context.Context, name string) (map[string]output.Value, error) {
	step, ok := r.steps[name]
	if !ok {
		return nil, fmt.Errorf("mcpserver: no step named %q", name)
	}
	ref, err := config.ParseStepRef(step.Uses)
	if err != nil {
		return nil, err
	}
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return nil, fmt.Errorf("mcpserver: plugin %q not loaded", ref.Plugin)
	}
	setupDeps, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return nil, err
	}
	with, err := r.renderWith(name, step, r.sameScopeDeps(name), setupDeps)
	if err != nil {
		return nil, err
	}
	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: name, Type: ref.Step, Env: r.env, Config: with,
	})
	if err != nil {
		return nil, fmt.Errorf("mcpserver: export %s: %w", name, err)
	}
	return valuesFromProto(resp.GetOut()), nil
}

// callTool runs tool - a namespaced name from collectTools - against the
// step named name. It rejects a step whose actual type doesn't match the
// tool's declared owner.
func (r *run) callTool(ctx context.Context, name, tool string, args json.RawMessage) (any, bool, string, error) {
	route, ok := r.toolRoutes[tool]
	if !ok {
		return nil, false, "", fmt.Errorf("mcpserver: no such tool %q", tool)
	}
	step, ok := r.steps[name]
	if !ok {
		return nil, false, "", fmt.Errorf("mcpserver: no step named %q", name)
	}
	ref, err := config.ParseStepRef(step.Uses)
	if err != nil {
		return nil, false, "", err
	}
	if ref.Plugin != route.plugin || ref.Step != route.step {
		return nil, false, "", fmt.Errorf("mcpserver: step %q is a %s step, not a %s:%s step", name, ref, route.plugin, route.step)
	}
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return nil, false, "", fmt.Errorf("mcpserver: plugin %q not loaded", ref.Plugin)
	}

	setupDeps, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return nil, false, "", err
	}
	deps := r.sameScopeDeps(name)
	with, err := r.renderWith(name, step, deps, setupDeps)
	if err != nil {
		return nil, false, "", err
	}

	resp, err := client.CallTool(ctx, &pb.ToolCallRequest{
		Step: name, Type: ref.Step, Env: r.env, Config: with,
		Deps: depsToProto(depsWithSetup(deps, setupDeps)),
		Tool: route.tool, Arguments: args,
	})
	if err != nil {
		return nil, false, "", fmt.Errorf("mcpserver: call tool %s on %s: %w", tool, name, err)
	}

	var content any
	if len(resp.GetContent()) > 0 {
		if err := json.Unmarshal(resp.GetContent(), &content); err != nil {
			return nil, false, "", fmt.Errorf("mcpserver: tool %s on %s: decode content: %w", tool, name, err)
		}
	}
	return content, resp.GetIsError(), resp.GetErrorMessage(), nil
}

// idempotent reports whether step's step type declares itself safe to call
// Up on again. An unparseable step.Uses reports false - the same as any
// step type that never declared IdempotentStep.
func (r *run) idempotent(step string) bool {
	if _, ok := r.groups[step]; ok {
		return true
	}
	ref, err := config.ParseStepRef(r.steps[step].Uses)
	if err != nil {
		return false
	}
	return stepIdempotent(r.caps[ref.Plugin], ref.Step)
}

// markSkipped marks every step in considered that dag.Walk (or WalkFrom)
// attempted but has neither a recorded output in results nor already
// reports Failed - a step whose dependency failed, or was itself skipped
// by that cascade.
func (r *run) markSkipped(considered []string, results map[string]dag.Outputs) {
	states := make(map[string]session.State, len(considered))
	for _, s := range r.store.Snapshot().Steps {
		states[s.Name] = s.State
	}
	for _, name := range considered {
		if _, ok := results[name]; ok {
			continue
		}
		if states[name] == session.Failed {
			continue
		}
		r.store.SetStep(name, session.Skipped, "")
	}
}

func (r *run) down(ctx context.Context) error {
	completed := r.snapshotCompleted()
	if len(completed) == 0 {
		return nil
	}

	// Remove only the steps (and groups) that came up, in reverse
	// dependency order - a name absent from completed never ran (or
	// failed), so it must not be walked here at all, not just have its
	// edges filtered.
	needs := r.downNeeds(completed)

	_, err := dag.New(needs).Reverse().Walk(ctx, func(ctx context.Context, name string, _ map[string]dag.Outputs) (dag.Outputs, error) {
		return r.downStep(ctx, name, needs[name], completed)
	})
	return err
}

// downNeeds builds down's reduced needs map: only the steps and groups
// that actually came up (are keys in completed), each with only the needs
// edges among those.
func (r *run) downNeeds(completed map[string]dag.Outputs) map[string][]string {
	needs := make(map[string][]string, len(completed))
	for name := range completed {
		var deps []string
		if grp, ok := r.groups[name]; ok {
			for _, member := range grp.Members {
				dotted := memberName(name, member)
				if _, ok := completed[dotted]; ok {
					deps = append(deps, dotted)
				}
			}
		} else {
			for _, dep := range r.steps[name].Needs {
				resolved, ok := r.resolveNeed(name, dep)
				if !ok {
					continue
				}
				if _, ok := completed[resolved]; ok {
					deps = append(deps, resolved)
				}
			}
		}
		needs[name] = deps
	}
	return needs
}

// downStep removes one step or group, the dag.NodeFunc down's reversed
// walk runs per node. deps is name's own downNeeds entry.
func (r *run) downStep(ctx context.Context, name string, deps []string, completed map[string]dag.Outputs) (dag.Outputs, error) {
	if _, ok := r.groups[name]; ok {
		r.store.SetStep(name, session.Removed, "")
		return nil, nil //nolint:nilnil // a nil dag.Outputs is a valid empty result, not the caller ever mistaking it for "not found"
	}

	step := r.steps[name]
	ref, refErr := config.ParseStepRef(step.Uses)
	if refErr != nil {
		return nil, refErr
	}
	client := r.plugins[ref.Plugin]

	if !stepImplementsDown(r.caps[ref.Plugin], ref.Step) {
		r.store.SetStep(name, session.Removed, "")
		return nil, nil //nolint:nilnil // a nil dag.Outputs is a valid empty result, not the caller ever mistaking it for "not found"
	}

	depOutputs := make(map[string]dag.Outputs, len(deps))
	for _, dep := range deps {
		depOutputs[dep] = completed[dep]
	}
	setupDeps, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		return nil, err
	}
	with, renderErr := r.renderWith(name, step, depOutputs, setupDeps)
	if renderErr != nil {
		return nil, renderErr
	}

	estimate, _ := r.timings.EstimateDown(name, ref.String())
	start := time.Now()
	stop := r.trackProgress(ctx, name, estimate)
	defer stop()

	r.emit(name, "down")
	r.store.SetStep(name, session.Removing, "")

	downErr := client.Down(ctx, &pb.DownRequest{
		Step:    name,
		Type:    ref.Step,
		Env:     r.env,
		Config:  with,
		Outputs: outputsToProto(completed[name]),
	}, r.onEvent(name))
	if downErr != nil {
		downMsg := uerr.Display(downErr)
		r.emit(name, "teardown failed: "+downMsg)
		r.store.SetStep(name, session.Failed, downMsg)
		return nil, downErr
	}
	r.emit(name, "removed")
	r.store.SetStep(name, session.Removed, "")
	r.timings.RecordDown(ctx, name, ref.String(), time.Since(start))
	return nil, nil //nolint:nilnil // a nil dag.Outputs is a valid empty result, not the caller ever mistaking it for "not found"
}

// onEvent returns a handler that renders the log and progress events of one
// plugin. A step's own log lines go to the web console and the durable log
// file, not to the terminal - the terminal only narrates kevin's own DAG
// progress (up/ready/failed/removed, elsewhere in this package).
func (r *run) onEvent(step string) func(*pb.Event) {
	return func(ev *pb.Event) {
		switch {
		case ev.GetLog() != nil:
			r.store.Log(step, ev.GetLog().GetStream(), ev.GetLog().GetText())
			r.stepLog.Info(ev.GetLog().GetText(), "step", step, "stream", ev.GetLog().GetStream())
		case ev.GetProgress() != nil:
			p := ev.GetProgress()
			if p.GetTotal() > 0 {
				r.emit(step, fmt.Sprintf("%s (%d/%d)", p.GetLabel(), p.GetCurrent(), p.GetTotal()))
				return
			}
			r.emit(step, p.GetLabel())
		}
	}
}

func (r *run) emit(step, text string) {
	_, _ = fmt.Fprintf(r.events, "%-16s %s\n", step, text)
}

func depsToProto(deps map[string]dag.Outputs) map[string]*pb.Outputs {
	if len(deps) == 0 {
		return nil
	}
	out := make(map[string]*pb.Outputs, len(deps))
	for name, values := range deps {
		out[name] = outputsToProto(values)
	}
	return out
}

// valuesFromProto lifts a step's proto outputs into a plain output.Value map.
func valuesFromProto(o *pb.Outputs) map[string]output.Value {
	values := o.GetValues()
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]output.Value, len(values))
	for k, v := range values {
		out[k] = output.Value{String: v.GetStringValue(), Sensitive: v.GetSensitive()}
	}
	return out
}

// outputsFromProto lifts a step's proto outputs into the DAG's Outputs map.
func outputsFromProto(o *pb.Outputs) dag.Outputs {
	values := valuesFromProto(o)
	if values == nil {
		return nil
	}
	out := make(dag.Outputs, len(values))
	for k, v := range values {
		out[k] = v
	}
	return out
}

// outputsToProto lowers the DAG's Outputs map back to the proto wire format.
func outputsToProto(values dag.Outputs) *pb.Outputs {
	if len(values) == 0 {
		return nil
	}
	out := &pb.Outputs{Values: make(map[string]*pb.Value, len(values))}
	for k, v := range values {
		val, _ := v.(output.Value)
		out.Values[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: val.String}, Sensitive: val.Sensitive}
	}
	return out
}

// stepExports reports whether info's step type name implements Export.
func stepExports(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Export
		}
	}
	return false
}

// exportCrossScopeStep asks a setup-scope step's plugin how to reach what
// it created - the same request exportStep makes against this session's
// already-running plugin, sent with the setup step's own unrendered with
// block, the same as exportStep. setupName is the name
// with the "setup." prefix already stripped.
func (r *run) exportCrossScopeStep(ctx context.Context, setupName string) (dag.Outputs, error) {
	v, err, _ := r.exportGroup.Do(setupName, func() (any, error) {
		if grp, ok := r.cfg.Groups(config.ScopeSetup)[setupName]; ok {
			return r.doExportCrossScopeGroup(ctx, setupName, grp)
		}
		return r.doExportCrossScopeStep(ctx, setupName)
	})
	if err != nil {
		return nil, err //nolint:wrapcheck // doExportCrossScopeStep already wraps its own errors; singleflight only relays them
	}
	outputs, _ := v.(dag.Outputs) // this Group's Do calls only ever return dag.Outputs
	return outputs, nil
}

// doExportCrossScopeStep is exportCrossScopeStep's uncached call.
func (r *run) doExportCrossScopeStep(ctx context.Context, setupName string) (dag.Outputs, error) {
	step := r.cfg.Setup[setupName]
	ref, err := config.ParseStepRef(step.Uses)
	if err != nil {
		return nil, err
	}
	client, ok := r.plugins[ref.Plugin]
	if !ok {
		return nil, fmt.Errorf("plugin %q not loaded", ref.Plugin)
	}
	if !stepExports(r.caps[ref.Plugin], ref.Step) {
		return nil, fmt.Errorf("setup step %q (%s) does not implement export", setupName, ref)
	}
	with, err := expr.Render(step.With, setupName, expr.Scopes{Project: r.project})
	if err != nil {
		return nil, fmt.Errorf("setup step %q: %w", setupName, err)
	}
	resp, err := client.Export(ctx, &pb.ExportRequest{
		Step: setupName, Type: ref.Step, Env: r.env, Config: with,
	})
	if err != nil {
		return nil, fmt.Errorf("export setup step %q: %w", setupName, err)
	}
	return outputsFromProto(resp.GetOut()), nil
}

// crossScopeDeps resolves every "setup."-prefixed entry in name's own
// needs list via Export, keyed by the unprefixed setup-step name - the
// shape both the "setup" CEL variable and the step's wire Deps use. A name
// with no such entry returns (nil, nil) - the common case, most steps use
// only same-scope needs.
func (r *run) crossScopeDeps(ctx context.Context, name string) (map[string]dag.Outputs, error) {
	var out map[string]dag.Outputs
	for _, dep := range r.steps[name].Needs {
		setupName, ok := strings.CutPrefix(dep, setupPrefix)
		if !ok {
			continue // same-scope, resolved by the dag walk already.
		}
		vals, err := r.exportCrossScopeStep(ctx, setupName)
		if err != nil {
			return nil, fmt.Errorf("%s: needs %q: %w", name, dep, err)
		}
		if out == nil {
			out = make(map[string]dag.Outputs)
		}
		out[setupName] = vals
	}
	return out, nil
}

// sameScopeDeps builds name's same-scope deps map from already-completed
// step outputs. Use it outside the DAG walk, such as from Export.
func (r *run) sameScopeDeps(name string) map[string]dag.Outputs {
	completed := r.snapshotCompleted()
	var out map[string]dag.Outputs
	for _, dep := range r.steps[name].Needs {
		if strings.HasPrefix(dep, setupPrefix) {
			continue // cross-scope, resolved by crossScopeDeps instead.
		}
		resolved, ok := r.resolveNeed(name, dep)
		if !ok {
			continue
		}
		outputs, ok := completed[resolved]
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[string]dag.Outputs)
		}
		out[resolved] = outputs
	}
	return out
}

// depsWithSetup folds setupDeps into deps for the wire Deps field, each
// entry keyed "setup.<name>". This is the one channel that still carries
// a value's Sensitive flag once it crosses scopes.
func depsWithSetup(deps, setupDeps map[string]dag.Outputs) map[string]dag.Outputs {
	if len(setupDeps) == 0 {
		return deps
	}
	out := make(map[string]dag.Outputs, len(deps)+len(setupDeps))
	maps.Copy(out, deps)
	for name, vals := range setupDeps {
		out[setupPrefix+name] = vals
	}
	return out
}

// executablePath resolves the path of the running binary. A builtin plugin
// runs this path with "plugin run <name>". A test overrides this variable,
// because os.Executable reports the test binary under go test.
var executablePath = os.Executable
