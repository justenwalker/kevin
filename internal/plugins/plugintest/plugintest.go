// Package plugintest helps a builtin plugin's tests feed it a with block the
// way the engine does.
package plugintest

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// Defaulted returns with, a step's rendered with block, checked against
// schema and carrying every default the schema declares. The engine sends a
// plugin its with block in this form.
func Defaulted(tb testing.TB, schema []byte, with string) []byte {
	tb.Helper()
	out, err := config.ApplySchema("test.with", []byte(with), schema)
	require.NoError(tb, err)
	return out
}
