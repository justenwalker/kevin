package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckLinks(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []brokenLink
	}{
		{
			name: "valid links",
			files: map[string]string{
				"index.html": `<a href="/docs/a/">a</a> <a href="docs/a/#intro">rel</a> <a href="/style.css">css</a>` +
					`<a href="/docs/a/index.html">explicit</a> <a href="/docs/a/?q=1#intro">query</a> <a href="#">top</a>` +
					`<a href="mailto:a@example.com">mail</a> <a href="//cdn.example.com/x.js">cdn</a> <a href="/404.html">404</a>`,
				"404.html":          `<p>not found</p>`,
				"docs/a/index.html": `<h2 id="intro">x</h2><a href="#intro">self</a> <a href=../b>b</a> <a href="https://example.com/#x">ext</a>`,
				"docs/b/index.html": `<p>b</p>`,
				"style.css":         `body{}`,
			},
		},
		{
			name: "escaped markup is not a link",
			files: map[string]string{
				"index.html": `<pre>&lt;a href="/gone/"&gt;x&lt;/a&gt;</pre><a href="#a&amp;b">amp</a><h2 id="a&amp;b">x</h2>`,
			},
		},
		{
			name: "missing page",
			files: map[string]string{
				"index.html": `<a href="/docs/gone/">x</a> <a href="/missing.css">y</a>`,
			},
			want: []brokenLink{
				{page: "/", href: "/docs/gone/", missing: "page"},
				{page: "/", href: "/missing.css", missing: "page"},
			},
		},
		{
			name: "missing anchor reported once per page",
			files: map[string]string{
				"index.html":        `<a href="/docs/a/#old">x</a> <a href="/docs/a/#old">again</a>`,
				"docs/a/index.html": `<h2 id='new'>x</h2><a href="#old">self</a>`,
			},
			want: []brokenLink{
				{page: "/", href: "/docs/a/#old", missing: "anchor"},
				{page: "/docs/a/", href: "#old", missing: "anchor"},
			},
		},
		{
			name: "malformed href",
			files: map[string]string{
				"index.html": `<a href="/%zz">x</a>`,
			},
			want: []brokenLink{{page: "/", href: "/%zz", missing: "valid URL"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checkLinks(writeSite(t, tt.files))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("missing directory", func(t *testing.T) {
		_, err := checkLinks(filepath.Join(t.TempDir(), "nope"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "docs-check: walk")
	})
}

// writeSite writes files, keyed by slash-separated path, under a new
// temporary directory and returns it.
func writeSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	}
	return dir
}
