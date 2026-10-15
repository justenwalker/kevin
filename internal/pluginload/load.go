// Package pluginload fetches, verifies and starts the plugins an environment
// declares, and reads their capabilities.
package pluginload

import (
	"context"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/step"
)

var log = logging.New("pluginload")

// Fetch reads the environment file and downloads and extracts every
// plugin package that a step of either scope references: everything
// Load does before it starts a plugin process. It starts no
// process and validates no schema, so it needs no plugin to actually run.
//
// Fetch returns the distinct, non-builtin plugin names it resolved,
// sorted.
func Fetch(ctx context.Context, dir, name string, tags []string) ([]string, error) {
	file, err := config.Load(dir, name, tags)
	if err != nil {
		return nil, err
	}

	specs, err := file.Plugins()
	if err != nil {
		return nil, err
	}

	names, err := file.StepPlugins()
	if err != nil {
		return nil, err
	}

	fetched := make([]string, 0, len(names))
	for _, n := range names {
		if n == config.Builtin {
			continue
		}
		if _, err := resolveSpec(ctx, n, file.Dir(), specs); err != nil {
			return fetched, err
		}
		fetched = append(fetched, n)
	}
	return fetched, nil
}

// Loaded is an environment read from disk with its plugins started.
type Loaded struct {
	// Config is the validated environment. It is nil when loading failed.
	Config *config.Config

	// Plugins holds every started plugin, keyed by name. The caller must close
	// them with [Loaded.Close], even when loading failed.
	Plugins map[string]*pluginhost.Client

	// Caps is what each plugin reported from Info, keyed by plugin name.
	Caps map[string]pluginhost.Info
}

// Options selects the environment [Load] reads.
type Options struct {
	// Dir is the project directory.
	Dir string

	// Name selects a named environment, or "" for the unnamed one.
	Name string

	// Tags are the CUE build tags to evaluate the environment with.
	Tags []string

	// Vars are the inputs for the environment's variables.
	Vars config.VariableInputs
}

// Load reads the environment file, starts every plugin that a step
// of either scope references, and validates the environment against the
// schemas of each started plugin.
//
// Load returns the plugins that did start even on failure. The
// caller must close them.
func Load(ctx context.Context, opts Options) (Loaded, error) {
	file, err := config.Load(opts.Dir, opts.Name, opts.Tags)
	if err != nil {
		return Loaded{}, err
	}

	specs, err := file.Plugins()
	if err != nil {
		return Loaded{}, err
	}

	names, err := file.StepPlugins()
	if err != nil {
		return Loaded{}, err
	}

	plugins, err := launchAll(ctx, file.Dir(), specs, names)
	if err != nil {
		return Loaded{Plugins: plugins}, err
	}

	caps, err := collectCaps(ctx, plugins)
	if err != nil {
		return Loaded{Plugins: plugins}, err
	}

	schemas := make(map[string]config.PluginSchemas, len(caps))
	for name, info := range caps {
		steps := make(map[string][]byte, len(info.Steps))
		export := make(map[string]bool, len(info.Steps))
		for _, st := range info.Steps {
			steps[st.Name] = st.Schema
			export[st.Name] = st.Export
		}
		schemas[name] = config.PluginSchemas{Config: info.Schema, Steps: steps, Export: export}
	}
	if err = file.Validate(schemas); err != nil {
		return Loaded{Plugins: plugins}, err
	}

	cfg, err := file.Config()
	if err != nil {
		return Loaded{Plugins: plugins}, err
	}
	if err = cfg.ResolveVariables(opts.Vars); err != nil {
		return Loaded{Plugins: plugins}, err
	}
	if err = cfg.ResolvePluginConfigs(schemas); err != nil {
		return Loaded{Plugins: plugins}, err
	}
	return Loaded{Config: cfg, Plugins: plugins, Caps: caps}, nil
}

// launchAll starts one process for every plugin name that a step of either
// scope references. Two step types of one plugin share the same process. On
// failure launchAll returns the plugins that did start, together with the
// error. The caller must stop them.
func launchAll(ctx context.Context, dir string, specs map[string]config.PluginSpec, names []string) (map[string]*pluginhost.Client, error) {
	clients := make(map[string]*pluginhost.Client, len(names))
	for _, name := range names {
		spec, err := resolveSpec(ctx, name, dir, specs)
		if err != nil {
			return clients, err
		}
		client, err := pluginhost.Launch(ctx, name, spec)
		if err != nil {
			return clients, err
		}
		clients[name] = client
	}
	return clients, nil
}

// Close stops every plugin process.
func (l Loaded) Close() {
	for _, client := range l.Plugins {
		client.Close()
	}
}

// Configure sends the config block of every plugin, once before any step of
// that plugin runs. A plugin with no config block still receives the call,
// with an empty config.
func (l Loaded) Configure(ctx context.Context, env step.Env) error {
	for name, client := range l.Plugins {
		if err := client.Configure(ctx, l.Config.Plugins[name].Config, env); err != nil {
			return err
		}
	}
	return nil
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
