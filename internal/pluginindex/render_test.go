package pluginindex_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/pluginindex"
)

func TestSnippet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	url := fixtureRepo(t, map[string]string{
		"plugins/demo/plugin.yaml": pluginMeta("demo", "a demo plugin"),
		"plugins/demo/versions/1.0.0.yaml": pluginVersion("1.0.0",
			"oci: ghcr.io/example/demo:v1.0.0\n  signing:\n    scheme: minisign"),
		"plugins/demo/versions/1.1.0.yaml": pluginVersion("1.1.0", "oci: ghcr.io/example/demo:v1.1.0"),
		"plugins/demo/versions/1.2.0.yaml": pluginVersion("1.2.0",
			"file: ./demo.tar\n  signing:\n    scheme: sigstore\n    identity: ci@example.com\n    issuer: https://token.actions.githubusercontent.com"),
		"plugins/demo/versions/1.3.0.yaml": pluginVersion("1.3.0", "http: https://example.com/demo.tar"),
	})
	_, err := pluginindex.AddSource(t.Context(), url, "demo-repo")
	require.NoError(t, err)

	catalog, err := pluginindex.LoadAll(t.Context())
	require.NoError(t, err)
	p, err := catalog.Resolve("demo")
	require.NoError(t, err)
	require.Len(t, p.Versions, 4)

	byVersion := map[string]pluginindex.Version{}
	for _, v := range p.Versions {
		byVersion[v.Version] = v
	}

	tests := []struct {
		version string
		want    string
	}{
		{
			version: "1.0.0",
			want: `plugins: {
	demo: {
		oci: "ghcr.io/example/demo:v1.0.0"
		signing: {
			scheme: "minisign"
		}
	}
}`,
		},
		{
			// Unset signing defaults to concrete false, not omitted.
			version: "1.1.0",
			want: `plugins: {
	demo: {
		oci:     "ghcr.io/example/demo:v1.1.0"
		signing: false
	}
}`,
		},
		{
			version: "1.2.0",
			want: `plugins: {
	demo: {
		file: "./demo.tar"
		signing: {
			scheme:   "sigstore"
			identity: "ci@example.com"
			issuer:   "https://token.actions.githubusercontent.com"
		}
	}
}`,
		},
		{
			version: "1.3.0",
			want: `plugins: {
	demo: {
		http:    "https://example.com/demo.tar"
		signing: false
	}
}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, err := pluginindex.Snippet(byVersion[tt.version], "demo")
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}
