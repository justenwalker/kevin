package pluginload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/pluginpkg"
	"github.com/justenwalker/kevin/internal/state"
)

// pluginPkgDir holds the extracted contents of every file: source plugin,
// one subdirectory per plugin name, inside the workspace.
const pluginPkgDir = "plugins"

// executablePath resolves the path of the running binary. A builtin plugin
// runs this path with "plugin run <name>". A test overrides this variable,
// because os.Executable reports the test binary under go test.
var executablePath = os.Executable

// resolveSpec turns the plugins: entry for name into a launchable spec,
// fetching, verifying and extracting its package first when it has one.
// cosignRunner runs the sigstore verification.
func resolveSpec(ctx context.Context, name, dir string, specs map[string]config.PluginSpec, cosignRunner command.Runner) (pluginhost.Spec, error) {
	if err := config.ResolvePlugin(name, specs); err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: %w", err)
	}
	if name == "builtin" {
		exe, err := executablePath()
		if err != nil {
			return pluginhost.Spec{}, fmt.Errorf("pluginload: locate the running binary: %w", err)
		}
		return pluginhost.Spec{Cmd: exe, Args: []string{"plugin", "run", name}, Dir: dir}, nil
	}

	spec := specs[name]
	src := sourceFor(spec, dir)
	if src == nil {
		return pluginhost.Spec{Cmd: spec.Cmd, Args: spec.Args, Env: spec.Env, Dir: dir}, nil
	}

	destDir := filepath.Join(state.For(dir, "").Workspace(), pluginPkgDir, name)
	pkg, err := src.fetch(ctx)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	if verifyErr := verifySource(ctx, src, pkg, spec.Signing, cosignRunner); verifyErr != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, friendlySignatureErr(verifyErr, name))
	}
	result, err := pluginpkg.Extract(pkg.Path, destDir, pkg.Digest)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// packagedSpec finishes a spec that pluginpkg.Extract already resolved, for
// either the file or the oci source: it cross-checks the package's declared
// name against the plugins: key, applies a plugins.<name>.args override, and
// builds the launchable Spec.
func packagedSpec(name string, spec config.PluginSpec, destDir string, result pluginpkg.Result) (pluginhost.Spec, error) {
	if result.Name != name {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: package declares name %q: %w",
			name, result.Name, pluginhost.ErrNameMismatch)
	}
	args := result.Args
	if len(spec.Args) > 0 {
		args = spec.Args
	}
	return pluginhost.Spec{Cmd: result.Cmd, Args: args, Env: spec.Env, Dir: destDir}, nil
}
