package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRunStripsTheProgramName proves run treats args[0] as the program name,
// not part of the command line: with no extra args, it must fall back to
// the default glob (relative to the repository root) and actually render
// every builtin plugin's reference page, rather than globbing against the
// program name itself and finding nothing.
func TestRunStripsTheProgramName(t *testing.T) {
	t.Chdir("../..")
	out := t.TempDir()
	rc := run(t.Context(), []string{"gen-reference-docs", "--out", out})
	require.Equal(t, 0, rc)
	assert.FileExists(t, out+"/container.md")
}
