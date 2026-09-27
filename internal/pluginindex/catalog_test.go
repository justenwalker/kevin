package pluginindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pluginindex"
)

func TestCatalogResolveAndSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	repoA := fixtureRepo(t, map[string]string{
		"plugins/postgres/plugin.yaml":         pluginMeta("postgres", "a postgres database"),
		"plugins/postgres/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/postgres:v1.0.0"),
		"plugins/shared/plugin.yaml":           pluginMeta("shared", "collides across two repos"),
		"plugins/shared/versions/1.0.0.yaml":   pluginVersion("1.0.0", "oci: ghcr.io/example/a/shared:v1.0.0"),
	})
	repoB := fixtureRepo(t, map[string]string{
		"plugins/redis/plugin.yaml":          pluginMeta("redis", "a redis cache"),
		"plugins/redis/versions/1.0.0.yaml":  pluginVersion("1.0.0", "oci: ghcr.io/example/redis:v1.0.0"),
		"plugins/shared/plugin.yaml":         pluginMeta("shared", "collides across two repos"),
		"plugins/shared/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/b/shared:v1.0.0"),
	})

	_, err := pluginindex.AddSource(t.Context(), repoA, "repo-a")
	require.NoError(t, err)
	_, err = pluginindex.AddSource(t.Context(), repoB, "repo-b")
	require.NoError(t, err)

	catalog, err := pluginindex.LoadAll(t.Context())
	require.NoError(t, err)

	t.Run("Resolve on an unambiguous bare name", func(t *testing.T) {
		p, err := catalog.Resolve("postgres")
		require.NoError(t, err)
		assert.Equal(t, "postgres", p.Name)
		assert.Equal(t, "repo-a", p.Repo.Alias)
	})

	t.Run("Resolve on a collision errors naming both aliases", func(t *testing.T) {
		_, err := catalog.Resolve("shared")
		require.ErrorIs(t, err, pluginindex.ErrAmbiguousPlugin)
		assert.Contains(t, err.Error(), "repo-a")
		assert.Contains(t, err.Error(), "repo-b")
	})

	t.Run("alias/name disambiguates a collision", func(t *testing.T) {
		p, err := catalog.Resolve("repo-a/shared")
		require.NoError(t, err)
		assert.Equal(t, "repo-a", p.Repo.Alias)

		p, err = catalog.Resolve("repo-b/shared")
		require.NoError(t, err)
		assert.Equal(t, "repo-b", p.Repo.Alias)
	})

	t.Run("Resolve on an unknown name", func(t *testing.T) {
		_, err := catalog.Resolve("nope")
		require.ErrorIs(t, err, pluginindex.ErrPluginNotFound)
	})

	t.Run("Search filters by name and summary, case-insensitively", func(t *testing.T) {
		names := pluginNames(catalog.Search("REDIS"))
		assert.Equal(t, []string{"redis"}, names)

		names = pluginNames(catalog.Search("database"))
		assert.Equal(t, []string{"postgres"}, names)

		names = pluginNames(catalog.Search("shared"))
		assert.ElementsMatch(t, []string{"shared", "shared"}, names, "both repos' shared entry shows up side by side")
	})

	t.Run("List reports every plugin, sorted by alias then name", func(t *testing.T) {
		list := catalog.List()
		require.Len(t, list, 4)
		for i := 1; i < len(list); i++ {
			prev, cur := list[i-1], list[i]
			assert.True(t, prev.Repo.Alias < cur.Repo.Alias ||
				(prev.Repo.Alias == cur.Repo.Alias && prev.Name <= cur.Name))
		}
	})
}

func pluginNames(plugins []pluginindex.Plugin) []string {
	out := make([]string, len(plugins))
	for i, p := range plugins {
		out[i] = p.Name
	}
	return out
}
