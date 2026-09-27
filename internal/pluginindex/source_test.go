package pluginindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pluginindex"
)

func TestAddListRemoveSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	url := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})

	res, err := pluginindex.AddSource(t.Context(), url, "")
	require.NoError(t, err)
	assert.Equal(t, url, res.Source.URL)
	assert.NotEmpty(t, res.Source.Alias)
	assert.Equal(t, 1, res.Plugins)
	assert.Empty(t, res.Warnings)

	sources, err := pluginindex.ListSources()
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Equal(t, res.Source, sources[0])

	require.NoError(t, pluginindex.RemoveSource(res.Source.Alias))
	sources, err = pluginindex.ListSources()
	require.NoError(t, err)
	assert.Empty(t, sources)
}

func TestAddSourceDuplicateURLIsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	url := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})

	first, err := pluginindex.AddSource(t.Context(), url, "myrepo")
	require.NoError(t, err)

	second, err := pluginindex.AddSource(t.Context(), url, "different-alias")
	require.NoError(t, err)
	assert.Equal(t, first.Source, second.Source, "duplicate URL is a silent no-op, existing source wins")

	sources, err := pluginindex.ListSources()
	require.NoError(t, err)
	assert.Len(t, sources, 1)
}

func TestAddSourceAliasCollision(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	urlA := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	urlB := fixtureRepo(t, map[string]string{
		"plugins/other/plugin.yaml":         pluginMeta("other", "another plugin"),
		"plugins/other/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/other:v1.0.0"),
	})

	_, err := pluginindex.AddSource(t.Context(), urlA, "shared")
	require.NoError(t, err)

	_, err = pluginindex.AddSource(t.Context(), urlB, "shared")
	require.ErrorIs(t, err, pluginindex.ErrAliasTaken)
}

func TestRemoveSourceUnknown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	err := pluginindex.RemoveSource("does-not-exist")
	require.ErrorIs(t, err, pluginindex.ErrUnknownSource)
}

func TestAddSourceRequiresIndexMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	writeFixtureFiles(t, dir, map[string]string{
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})
	commitFixtureRepo(t, dir, "fixture, no kevin-index.yaml")

	_, err := pluginindex.AddSource(t.Context(), dir, "no-marker")
	require.ErrorIs(t, err, pluginindex.ErrIndexMarkerMissing)
}

func TestAddSourceRejectsUnknownLayout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	url := fixtureRepo(t, map[string]string{
		"kevin-index.yaml":                 "layout: 2\n",
		"plugins/demo/plugin.yaml":         pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0", "oci: ghcr.io/example/demo:v1.0.0"),
	})

	_, err := pluginindex.AddSource(t.Context(), url, "bad-layout")
	require.Error(t, err)
}
