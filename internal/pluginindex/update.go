package pluginindex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"cuelang.org/go/cue"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/command/git"
)

// AddResult reports the outcome of [AddSource]: how many plugins the
// source's plugins/ tree holds right after it was cloned, and any
// per-plugin problems found along the way.
type AddResult struct {
	Source   Source
	Plugins  int
	Warnings []string
}

// UpdateResult reports one configured [Source]'s outcome from [Update].
type UpdateResult struct {
	Source   Source
	Plugins  int
	Warnings []string

	// Err is the source's own clone/load failure, if any. It does not stop
	// Update from attempting every other source.
	Err error
}

// Update re-clones every configured [Source] from scratch into a temp
// directory and atomically swaps it in, independently - one source's
// failure is reported on its own [UpdateResult] and never blocks another
// source from updating. Update's own error return is reserved for a
// failure outside any one source, such as being unable to read the sources
// directory at all.
func Update(ctx context.Context) ([]UpdateResult, error) {
	sources, err := ListSources()
	if err != nil {
		return nil, err
	}

	results := make([]UpdateResult, 0, len(sources))
	for _, src := range sources {
		plugins, warnings, err := syncSource(ctx, src)
		results = append(results, UpdateResult{Source: src, Plugins: plugins, Warnings: warnings, Err: err})
	}
	return results, nil
}

// syncSource clones src's repo into its cache dir (see [cloneAndSwap])
// and loads its plugins/ tree to count plugins and collect per-plugin
// warnings. A single bad plugin.yaml, bad versions/*.yaml, or unreachable
// VersionSource is a warning, not a failure.
func syncSource(ctx context.Context, src Source) (int, []string, error) {
	if err := cloneAndSwap(ctx, src.URL, src.repoDir()); err != nil {
		return 0, nil, err
	}
	plugins, warnings, err := loadRepoPlugins(ctx, src.repoDir(), true)
	if err != nil {
		return 0, nil, err
	}
	return len(plugins), warnings, nil
}

// syncVersionSource clones url's repo into its cache dir (see
// versionSourceDir), the same atomic swap [syncSource] uses for an
// index [Source]'s own clone, and returns the clone's local path.
func syncVersionSource(ctx context.Context, url string) (string, error) {
	dest := versionSourceDir(url)
	if err := cloneAndSwap(ctx, url, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// cloneAndSwap clones url into a fresh temp directory next to destDir,
// then atomically renames it over destDir - the shared clone mechanism
// both an index [Source] and a plugin's federated VersionSource use.
func cloneAndSwap(ctx context.Context, url, destDir string) error {
	parent := filepath.Dir(destDir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("pluginindex: create %q: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, "clone-*")
	if err != nil {
		return fmt.Errorf("pluginindex: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp) //nolint:errcheck // best-effort cleanup; the rename below empties it on success

	cloneDir := filepath.Join(tmp, "repo")
	if cloneErr := git.New(command.Default).Clone(ctx, git.CloneSpec{URL: url, Dir: cloneDir}); cloneErr != nil {
		return fmt.Errorf("pluginindex: clone %q: %w", url, cloneErr)
	}

	if rmErr := os.RemoveAll(destDir); rmErr != nil {
		return fmt.Errorf("pluginindex: remove %q: %w", destDir, rmErr)
	}
	if renameErr := os.Rename(cloneDir, destDir); renameErr != nil {
		return fmt.Errorf("pluginindex: install clone of %q: %w", url, renameErr)
	}
	return nil
}

// validateIndexMarker reads and validates repoDir's kevin-index.yaml
// against #Index.
func validateIndexMarker(repoDir string) error {
	path := filepath.Join(repoDir, "kevin-index.yaml")
	src, err := os.ReadFile(path) //nolint:gosec // repoDir is a locally cloned index repo path, not user input
	if os.IsNotExist(err) {
		return fmt.Errorf("pluginindex: %q: %w", repoDir, ErrIndexMarkerMissing)
	}
	if err != nil {
		return fmt.Errorf("pluginindex: read %q: %w", path, err)
	}
	_, err = validateAgainst(cue.ParsePath("#Index"), path, src)
	return err
}

// loadRepoPlugins loads every plugins/<name> directory under repoDir. A
// repoDir that has never been cloned yet reports no plugins, not an
// error - the same graceful "nothing here yet" repoDir already had
// before kevin-index.yaml existed. A repoDir that exists but fails its
// marker check, or a plugin directory that fails to load (bad
// plugin.yaml, bad versions/*.yaml, or - only when sync is true - an
// unreachable/unverifiable VersionSource) is skipped and reported as a
// warning instead of failing the whole repo. sync controls whether a
// plugin's own VersionSource is cloned (true, from Update/syncSource) or
// only read from whatever's already cached (false, from LoadAll, which
// never clones or fetches anything - see its own doc comment).
func loadRepoPlugins(ctx context.Context, repoDir string, sync bool) ([]Plugin, []string, error) {
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err := validateIndexMarker(repoDir); err != nil {
		return nil, nil, err
	}

	pluginsDir := filepath.Join(repoDir, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("pluginindex: read %q: %w", pluginsDir, err)
	}

	var plugins []Plugin
	var warnings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(pluginsDir, e.Name())
		p, loadErr := loadPluginVersioned(ctx, dir, sync)
		if loadErr != nil {
			warnings = append(warnings, loadErr.Error())
			continue
		}
		if len(p.Versions) == 0 {
			warnings = append(warnings, fmt.Sprintf("pluginindex: %q: no versions found", dir))
		}
		warnings = append(warnings, uncoveredSignerWarnings(dir, p)...)
		plugins = append(plugins, p)
	}
	return plugins, warnings, nil
}

// loadPluginVersioned loads dir's plugin.yaml, then its versions - see
// [resolveVersionsDir] for where those versions come from.
func loadPluginVersioned(ctx context.Context, dir string, sync bool) (Plugin, error) {
	p, err := loadPluginMeta(dir)
	if err != nil {
		return Plugin{}, err
	}

	versionsDir, signers, err := resolveVersionsDir(ctx, dir, p, sync)
	if err != nil {
		return Plugin{}, err
	}

	p.Versions, err = loadVersions(ctx, versionsDir, signers)
	if err != nil {
		return Plugin{}, err
	}
	return p, nil
}

// resolveVersionsDir reports where p's versions live - dir's own
// versions/ subdirectory when p.VersionSource is unset (nil signers, no
// signature required), or p.VersionSource's own clone when it is set
// (p.Signers, a mandatory signature check - see loadVersions): synced
// first when sync is true, or read from whatever's already cached when
// false, and its own kevin-index.yaml marker validated whenever it
// exists on disk either way. Returns ErrVersionSourceNoSigners when
// VersionSource is set but Signers is empty.
func resolveVersionsDir(ctx context.Context, dir string, p Plugin, sync bool) (string, []Signer, error) {
	if p.VersionSource == "" {
		return filepath.Join(dir, "versions"), nil, nil
	}
	if len(p.Signers) == 0 {
		return "", nil, fmt.Errorf("pluginindex: %q: %w", dir, ErrVersionSourceNoSigners)
	}

	srcDir := versionSourceDir(p.VersionSource)
	if sync {
		var err error
		srcDir, err = syncVersionSource(ctx, p.VersionSource)
		if err != nil {
			return "", nil, fmt.Errorf("pluginindex: %q: version source %q: %w", dir, p.VersionSource, err)
		}
	}
	if _, err := os.Stat(srcDir); err == nil {
		if err := validateIndexMarker(srcDir); err != nil {
			return "", nil, fmt.Errorf("pluginindex: %q: version source %q: %w", dir, p.VersionSource, err)
		}
	}
	return filepath.Join(srcDir, "plugins", p.Name, "versions"), p.Signers, nil
}

// uncoveredSignerWarnings reports a warning for every version whose
// signing scheme has no matching entry in p.Signers - a gap that would
// otherwise only surface when "kevin plugin index install" tries, and
// fails, to trust a nonexistent signer.
func uncoveredSignerWarnings(dir string, p Plugin) []string {
	var warnings []string
	for _, v := range p.Versions {
		if v.Source.Signing == nil {
			continue
		}
		if len(p.SignersFor(v.Source.Signing.Scheme)) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"pluginindex: %q: version %s signs with %s but plugin.yaml declares no matching signer",
				dir, v.Version, v.Source.Signing.Scheme))
		}
	}
	return warnings
}
