package pluginindex

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Catalog is every plugin found across every configured [Source]'s cloned
// repo, as of the last [Update].
type Catalog struct {
	plugins []Plugin
}

// LoadAll reads every configured Source's cached repo (see [Update]) and
// builds a Catalog from whatever plugins/ trees are already on disk. It
// does not clone or fetch anything itself - a federated
// Plugin.VersionSource is read from its own cache the same way, never
// synced here. ctx is only used to verify an already-cloned federated
// version's sigstore signature, which shells out to cosign.
func LoadAll(ctx context.Context) (Catalog, error) {
	sources, err := ListSources()
	if err != nil {
		return Catalog{}, err
	}

	var plugins []Plugin
	for _, src := range sources {
		found, _, err := loadRepoPlugins(ctx, src.repoDir(), false)
		if err != nil {
			return Catalog{}, err
		}
		for i := range found {
			found[i].Repo = src
		}
		plugins = append(plugins, found...)
	}
	return Catalog{plugins: plugins}, nil
}

// List reports every plugin in the Catalog, sorted by alias then name.
func (c Catalog) List() []Plugin {
	out := append([]Plugin(nil), c.plugins...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo.Alias != out[j].Repo.Alias {
			return out[i].Repo.Alias < out[j].Repo.Alias
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Search reports every plugin in the Catalog whose name or summary
// contains query, case-insensitively, sorted by alias then name.
func (c Catalog) Search(query string) []Plugin {
	q := strings.ToLower(query)
	var out []Plugin
	for _, p := range c.List() {
		if strings.Contains(strings.ToLower(p.Name), q) || strings.Contains(strings.ToLower(p.Summary), q) {
			out = append(out, p)
		}
	}
	return out
}

// Resolve looks up nameOrRef, either a bare "<name>" or a scoped
// "<alias>/<name>".
//
// A bare name matching a plugin in more than one configured source returns
// [ErrAmbiguousPlugin], naming every matching alias. Resolve returns
// [ErrPluginNotFound] when nothing matches.
func (c Catalog) Resolve(nameOrRef string) (Plugin, error) {
	if alias, name, ok := strings.Cut(nameOrRef, "/"); ok {
		for _, p := range c.plugins {
			if p.Repo.Alias == alias && p.Name == name {
				return p, nil
			}
		}
		return Plugin{}, fmt.Errorf("pluginindex: %q: %w", nameOrRef, ErrPluginNotFound)
	}

	var matches []Plugin
	for _, p := range c.plugins {
		if p.Name == nameOrRef {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return Plugin{}, fmt.Errorf("pluginindex: %q: %w", nameOrRef, ErrPluginNotFound)
	case 1:
		return matches[0], nil
	default:
		aliases := make([]string, len(matches))
		for i, p := range matches {
			aliases[i] = p.Repo.Alias
		}
		return Plugin{}, fmt.Errorf("pluginindex: %q found in %s, retype as <alias>/%s: %w",
			nameOrRef, strings.Join(aliases, ", "), nameOrRef, ErrAmbiguousPlugin)
	}
}
