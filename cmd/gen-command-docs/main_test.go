package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunStripsTheProgramName proves run treats args[0] as the program name,
// not part of the command line: with no extra args, it must render every
// top-level kevin command's reference page (relative to the repository
// root) rather than treating the program name as a positional argument and
// failing cobra.NoArgs.
func TestRunStripsTheProgramName(t *testing.T) {
	t.Chdir("../..")
	out := t.TempDir()
	rc := run(t.Context(), []string{"gen-command-docs", "--out", out})
	require.Equal(t, 0, rc)
	assert.FileExists(t, out+"/ca.md")
	assert.FileExists(t, out+"/run.md")
}
