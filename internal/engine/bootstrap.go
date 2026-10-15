package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/engines"
	"github.com/justenwalker/kevin/internal/pluginload"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/state"
	"github.com/justenwalker/kevin/internal/termui"
)

// host is what Run and Teardown both need before they can touch a step: the
// loaded environment with its plugins running, and the container runtime.
type host struct {
	pluginload.Loaded

	Runtime   cri.Runtime
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
	loaded, err := pluginload.Load(ctx, opts.Dir, opts.Name, opts.Tags, config.VariableInputs{File: opts.VarFile, Set: opts.Vars})
	if err != nil {
		loaded.Close()
		return nil, err
	}
	rt, err := engines.New(opts.Engine)
	if err != nil {
		loaded.Close()
		return nil, err
	}
	return &host{
		Loaded:    loaded,
		Runtime:   rt,
		Workspace: filepath.Join(loaded.Config.Dir, state.WorkspaceDir, loaded.Config.Name),
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
	r.project = ca.ProjectVars(h.Config.Dir, h.Config.Name)
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
