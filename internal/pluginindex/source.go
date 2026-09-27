package pluginindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/config"
)

// RootDir is ~/.kevin/plugin-index - global, shared across every project.
func RootDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".kevin", "plugin-index")
	}
	return filepath.Join(home, ".kevin", "plugin-index")
}

// sourcesDir holds one JSON file per configured [Source], named by the
// sha256 of its URL so renaming an alias never touches the repo cache.
func sourcesDir() string { return filepath.Join(RootDir(), "sources") }

// reposDir holds one clone per configured [Source], named the same way.
func reposDir() string { return filepath.Join(RootDir(), "repos") }

// versionSourcesDir holds one clone per distinct Plugin.VersionSource
// URL, named by its sha256 the same way reposDir() names an index
// Source's own clone - shared across every plugin that happens to point
// at the same version-source repo.
func versionSourcesDir() string { return filepath.Join(RootDir(), "version-sources") }

// versionSourceDir is the local clone path for a Plugin.VersionSource
// URL, without cloning or checking that it exists yet.
func versionSourceDir(url string) string {
	return filepath.Join(versionSourcesDir(), sourceKey(url))
}

// sourceKey derives the content-addressed name a Source's URL is stored
// and cloned under.
func sourceKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// Source is one configured index repo.
type Source struct {
	Alias string `json:"alias"`
	URL   string `json:"url"`
}

// repoDir is the directory [syncSource] clones this Source's repo into.
func (s Source) repoDir() string {
	return filepath.Join(reposDir(), sourceKey(s.URL))
}

func sourceFile(url string) string {
	return filepath.Join(sourcesDir(), sourceKey(url)+".json")
}

// AddSource configures url as an index repo, aliased as alias (or, when
// alias is empty, a name derived from url via [config.SlugName]), then
// clones and loads it immediately, reporting its plugin count. Adding a
// URL that is already configured is a silent no-op: it returns the
// existing Source, with no re-clone and no Plugins/Warnings.
//
// Returns [ErrAliasTaken] when alias already names a different source.
func AddSource(ctx context.Context, url, alias string) (AddResult, error) {
	if err := ctx.Err(); err != nil {
		return AddResult{}, fmt.Errorf("pluginindex: %w", err)
	}

	if existing, ok, err := readSource(url); err != nil {
		return AddResult{}, err
	} else if ok {
		return AddResult{Source: existing}, nil
	}

	if alias == "" {
		alias = config.SlugName(url)
	}
	sources, err := ListSources()
	if err != nil {
		return AddResult{}, err
	}
	for _, s := range sources {
		if s.Alias == alias {
			return AddResult{}, fmt.Errorf("pluginindex: alias %q: %w", alias, ErrAliasTaken)
		}
	}

	src := Source{Alias: alias, URL: url}
	if writeErr := writeSource(src); writeErr != nil {
		return AddResult{}, writeErr
	}

	plugins, warnings, err := syncSource(ctx, src)
	if err != nil {
		return AddResult{}, err
	}
	return AddResult{Source: src, Plugins: plugins, Warnings: warnings}, nil
}

// readSource reports the already-configured [Source] for url, if any.
func readSource(url string) (Source, bool, error) {
	data, err := os.ReadFile(sourceFile(url))
	if os.IsNotExist(err) {
		return Source{}, false, nil
	}
	if err != nil {
		return Source{}, false, fmt.Errorf("pluginindex: read %q: %w", sourceFile(url), err)
	}
	var src Source
	if err := json.Unmarshal(data, &src); err != nil {
		return Source{}, false, fmt.Errorf("pluginindex: decode %q: %w", sourceFile(url), err)
	}
	return src, true, nil
}

// writeSource persists src to its content-addressed file.
func writeSource(src Source) error {
	if err := os.MkdirAll(sourcesDir(), 0o700); err != nil {
		return fmt.Errorf("pluginindex: create %q: %w", sourcesDir(), err)
	}
	data, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("pluginindex: encode source: %w", err)
	}
	path := sourceFile(src.URL)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("pluginindex: write %q: %w", path, err)
	}
	return nil
}

// ListSources reports every configured [Source]. A store that does not
// exist yet reports no sources, not an error.
func ListSources() ([]Source, error) {
	entries, err := os.ReadDir(sourcesDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pluginindex: read %q: %w", sourcesDir(), err)
	}
	out := make([]Source, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(sourcesDir(), e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // path is built from ListSources' own directory listing
		if err != nil {
			return nil, fmt.Errorf("pluginindex: read %q: %w", path, err)
		}
		var src Source
		if err := json.Unmarshal(data, &src); err != nil {
			return nil, fmt.Errorf("pluginindex: decode %q: %w", path, err)
		}
		out = append(out, src)
	}
	return out, nil
}

// RemoveSource deletes the configured source matching aliasOrURL - its
// alias, or its URL - along with its cloned repo cache.
//
// Returns [ErrUnknownSource] when nothing matches.
func RemoveSource(aliasOrURL string) error {
	sources, err := ListSources()
	if err != nil {
		return err
	}
	for _, s := range sources {
		if s.Alias != aliasOrURL && s.URL != aliasOrURL {
			continue
		}
		if err := os.Remove(sourceFile(s.URL)); err != nil {
			return fmt.Errorf("pluginindex: remove %q: %w", sourceFile(s.URL), err)
		}
		if err := os.RemoveAll(s.repoDir()); err != nil {
			return fmt.Errorf("pluginindex: remove %q: %w", s.repoDir(), err)
		}
		return nil
	}
	return fmt.Errorf("pluginindex: %q: %w", aliasOrURL, ErrUnknownSource)
}
