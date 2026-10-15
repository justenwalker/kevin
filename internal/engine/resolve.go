package engine

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
)

func resolveSpec(ctx context.Context, name, dir string, specs map[string]config.PluginSpec) (pluginhost.Spec, error) {
	if err := config.ResolvePlugin(name, specs); err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: %w", err)
	}
	if name == "builtin" {
		exe, err := executablePath()
		if err != nil {
			return pluginhost.Spec{}, fmt.Errorf("supervisor: locate the running binary: %w", err)
		}
		return pluginhost.Spec{Cmd: exe, Args: []string{"plugin", "run", name}, Dir: dir}, nil
	}

	spec := specs[name]
	destDir := filepath.Join(dir, WorkspaceDir, PluginPkgDir, name)
	switch {
	case spec.File != "":
		return resolveFileSpec(ctx, name, dir, destDir, spec)
	case spec.OCI != "":
		return resolveOCISpec(ctx, name, destDir, spec)
	case spec.HTTP != "":
		return resolveHTTPSpec(ctx, name, destDir, spec)
	}
	return pluginhost.Spec{Cmd: spec.Cmd, Args: spec.Args, Env: spec.Env, Dir: dir}, nil
}

// resolveFileSpec extracts a file: package, verifying its signature first
// when spec.Signing is set.
func resolveFileSpec(ctx context.Context, name, dir, destDir string, spec config.PluginSpec) (pluginhost.Spec, error) {
	pkgPath := spec.File
	if !filepath.IsAbs(pkgPath) {
		pkgPath = filepath.Join(dir, pkgPath)
	}
	if err := verifyFileSignature(ctx, pkgPath, spec.Signing); err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, friendlySignatureErr(err, name))
	}
	result, err := pluginpkg.Extract(pkgPath, destDir, spec.Checksum)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// resolveOCISpec fetches and extracts an oci: package, verifying its
// signature first when spec.Signing is set.
func resolveOCISpec(ctx context.Context, name, destDir string, spec config.PluginSpec) (pluginhost.Spec, error) {
	pkg, err := ocipkg.Fetch(ctx, spec.OCI)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, err)
	}
	if verifyErr := verifyOCISignature(ctx, spec.OCI, pkg.Digest, pkg.Path, spec.Signing); verifyErr != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, friendlySignatureErr(verifyErr, name))
	}
	result, err := pluginpkg.Extract(pkg.Path, destDir, pkg.Digest)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// resolveHTTPSpec fetches and extracts an http: package, verifying its
// signature first when spec.Signing is set.
func resolveHTTPSpec(ctx context.Context, name, destDir string, spec config.PluginSpec) (pluginhost.Spec, error) {
	pkg, err := httppkg.Fetch(ctx, spec.HTTP, spec.Checksum)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, err)
	}
	if verifyErr := verifyHTTPSignature(ctx, spec.HTTP, pkg.Path, spec.Signing); verifyErr != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, friendlySignatureErr(verifyErr, name))
	}
	result, err := pluginpkg.Extract(pkg.Path, destDir, pkg.Digest)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// packagedSpec finishes a spec that pluginpkg.Extract already resolved, for
// either the file or the oci source: it cross-checks the package's declared
// name against the plugins: key, applies a plugins.<name>.args override, and
// builds the launchable Spec.
func packagedSpec(name string, spec config.PluginSpec, destDir string, result pluginpkg.Result) (pluginhost.Spec, error) {
	if result.Name != name {
		return pluginhost.Spec{}, fmt.Errorf("supervisor: plugins.%s: package declares name %q: %w",
			name, result.Name, pluginhost.ErrNameMismatch)
	}
	args := result.Args
	if len(spec.Args) > 0 {
		args = spec.Args
	}
	return pluginhost.Spec{Cmd: result.Cmd, Args: args, Env: spec.Env, Dir: destDir}, nil
}

// FetchPlugins reads the environment file and downloads and extracts every
// plugin package that a step of either scope references: everything
// LoadAndLaunch does before it starts a plugin process. It starts no
// process and validates no schema, so it needs no plugin to actually run.
//
// FetchPlugins returns the distinct, non-builtin plugin names it resolved,
// sorted.
func FetchPlugins(ctx context.Context, dir, name string, tags []string) ([]string, error) {
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
	// them with [CloseAll], even when loading failed.
	Plugins map[string]*pluginhost.Client

	// Caps is what each plugin reported from Info, keyed by plugin name.
	Caps map[string]pluginhost.Info
}

// LoadAndLaunch reads the environment file, starts every plugin that a step
// of either scope references, and validates the environment against the
// schemas of each started plugin.
//
// LoadAndLaunch returns the plugins that did start even on failure. The
// caller must close them.
func LoadAndLaunch(ctx context.Context, dir, name string, tags []string, vars config.VariableInputs) (Loaded, error) {
	file, err := config.Load(dir, name, tags)
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
	if err = cfg.ResolveVariables(vars); err != nil {
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

// CloseAll stops every plugin process in clients.
func CloseAll(clients map[string]*pluginhost.Client) {
	for _, client := range clients {
		client.Close()
	}
}
