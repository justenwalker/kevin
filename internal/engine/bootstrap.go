package engine

import (
	"context"
	"io"
	"os"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/container/engines"
	"github.com/justenwalker/kevin/internal/plugins/load"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/statedir"
	"github.com/justenwalker/kevin/internal/ui/termui"
)

// host is what Run and Teardown both need before they can touch a step: the
// loaded environment with its plugins running, and the container runtime.
type host struct {
	load.Loaded

	Runtime   hostRuntime
	Workspace string

	// Events is where step output goes, resolved from [Options.Events].
	Events io.Writer

	live bool
}

// bootstrap loads the environment, starts its plugins and picks the
// container runtime. The caller must Close the host. On error everything
// bootstrap started is already stopped.
func bootstrap(ctx context.Context, opts Options) (*host, error) {
	live := wantsLiveUI(ctx, opts)
	loaded, err := load.Load(ctx, load.Options{
		Dir: opts.Dir, Name: opts.Name, Tags: opts.Tags,
		Vars: config.VariableInputs{File: opts.VarFile, Set: opts.Vars},
	})
	if err != nil {
		loaded.Close()
		return nil, err
	}
	return &host{
		Loaded:    loaded,
		Runtime:   engines.For(opts.Engine),
		Workspace: statedir.For(loaded.Config.Dir, loaded.Config.Name).Workspace(),
		Events:    eventsWriter(opts, live),
		live:      live,
	}, nil
}

// Close stops the plugins.
func (h *host) Close() {
	h.Loaded.Close()
}

// startUI starts the terminal step list when the terminal wants one, and
// returns the func that stops it.
func (h *host) startUI(ctx context.Context, store *session.Store) func() {
	if !h.live {
		return func() {}
	}
	return termui.New(os.Stderr).Start(ctx, store)
}

// fill sets the parts of r that come from the loaded environment.
func (r *run) fill(h *host) {
	r.steps = h.Config.Steps(r.scope)
	r.groups = h.Config.Groups(r.scope)
	r.plugins = stepPlugins(h.Plugins)
	r.caps = h.Caps
	r.project = statedir.For(h.Config.Dir, h.Config.Name).Vars()
}

// stopRelayIfAlone closes rl unless the other scope is still live and
// sharing it.
func (r *run) stopRelayIfAlone(ctx context.Context, rl trafficRelay) error {
	otherLive, err := r.otherScopeLive(ctx)
	if err != nil || len(otherLive) > 0 {
		return err
	}
	return rl.Close()
}
