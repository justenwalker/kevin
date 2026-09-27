package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRun(t *testing.T) {
	t.Run("usage", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 2, run([]string{"docs-check"}, &stdout, &stderr))
		assert.Contains(t, stderr.String(), "usage")
	})

	t.Run("clean", func(t *testing.T) {
		dir := writeSite(t, map[string]string{"index.html": `<a href="/">home</a>`})
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 0, run([]string{"docs-check", dir}, &stdout, &stderr))
		assert.Empty(t, stdout.String())
	})

	t.Run("unreadable site", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 1, run([]string{"docs-check", t.TempDir() + "/nope"}, &stdout, &stderr))
		assert.Empty(t, stdout.String())
		assert.Contains(t, stderr.String(), "docs-check: walk")
	})

	t.Run("broken", func(t *testing.T) {
		dir := writeSite(t, map[string]string{"index.html": `<a href="/nope/">x</a>`})
		var stdout, stderr bytes.Buffer
		assert.Equal(t, 1, run([]string{"docs-check", dir}, &stdout, &stderr))
		assert.Equal(t, "/: /nope/: missing page\n", stdout.String())
		assert.Contains(t, stderr.String(), "1 broken link")
	})
}
