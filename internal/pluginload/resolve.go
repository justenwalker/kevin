package pluginload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/httppkg"
	"github.com/justenwalker/kevin/internal/ocipkg"
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
func resolveSpec(ctx context.Context, name, dir string, specs map[string]config.PluginSpec) (pluginhost.Spec, error) {
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
	destDir := filepath.Join(dir, state.WorkspaceDir, pluginPkgDir, name)
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
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, friendlySignatureErr(err, name))
	}
	result, err := pluginpkg.Extract(pkgPath, destDir, spec.Checksum)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// resolveOCISpec fetches and extracts an oci: package, verifying its
// signature first when spec.Signing is set.
func resolveOCISpec(ctx context.Context, name, destDir string, spec config.PluginSpec) (pluginhost.Spec, error) {
	pkg, err := ocipkg.Fetch(ctx, spec.OCI)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	if verifyErr := verifyOCISignature(ctx, spec.OCI, pkg.Digest, pkg.Path, spec.Signing); verifyErr != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, friendlySignatureErr(verifyErr, name))
	}
	result, err := pluginpkg.Extract(pkg.Path, destDir, pkg.Digest)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	return packagedSpec(name, spec, destDir, result)
}

// resolveHTTPSpec fetches and extracts an http: package, verifying its
// signature first when spec.Signing is set.
func resolveHTTPSpec(ctx context.Context, name, destDir string, spec config.PluginSpec) (pluginhost.Spec, error) {
	pkg, err := httppkg.Fetch(ctx, spec.HTTP, spec.Checksum)
	if err != nil {
		return pluginhost.Spec{}, fmt.Errorf("pluginload: plugins.%s: %w", name, err)
	}
	if verifyErr := verifyHTTPSignature(ctx, spec.HTTP, pkg.Path, spec.Signing); verifyErr != nil {
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
