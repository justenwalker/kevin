package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun proves run treats args[0] as the program name, not part of the
// command line, for each subcommand: with no extra args beyond the
// subcommand name, reference falls back to its default glob and commands
// walks the real cobra tree, both relative to the repository root.
func TestRun(t *testing.T) {
	t.Run("reference", func(t *testing.T) {
		t.Chdir("../..")
		out := t.TempDir()
		rc := run(t.Context(), []string{"gen-docs", "reference", "--out", out})
		require.Equal(t, 0, rc)
		assert.FileExists(t, out+"/container.md")
	})

	t.Run("commands", func(t *testing.T) {
		t.Chdir("../..")
		out := t.TempDir()
		rc := run(t.Context(), []string{"gen-docs", "commands", "--out", out})
		require.Equal(t, 0, rc)
		assert.FileExists(t, out+"/ca.md")
		assert.FileExists(t, out+"/run.md")
	})
}
