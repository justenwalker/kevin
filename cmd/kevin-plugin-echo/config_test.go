package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/plugin"
)

func TestDemoIconIsAValidSmallPNG(t *testing.T) {
	got := demoIcon()
	require.NotEmpty(t, got)

	_, err := png.Decode(bytes.NewReader(got))
	require.NoError(t, err, "the console must be able to decode this as a real PNG")

	// Mirrors internal/console's own maxIconBytes cap, so this demo never
	// accidentally exercises the "too large, dropped" path.
	const maxIconBytes = 64 * 1024
	assert.Less(t, len(got), maxIconBytes)
}

func TestConfigure(t *testing.T) {
	t.Run("stores the greeting for a step to log", func(t *testing.T) {
		t.Cleanup(func() { require.NoError(t, configure(t.Context(), nil, plugin.Env{})) })

		require.NoError(t, configure(t.Context(), []byte(`{"greeting":"hi"}`), plugin.Env{}))
		assert.Equal(t, "hi", currentGreeting())

		out := &capture{}
		_, err := echo{}.Up(t.Context(), &plugin.UpRequest{Step: "a"}, out)
		require.NoError(t, err)
		assert.Contains(t, out.logs, "provider greeting: hi", "the step must log the configured greeting")
	})

	t.Run("with no data clears the greeting", func(t *testing.T) {
		t.Cleanup(func() { require.NoError(t, configure(t.Context(), nil, plugin.Env{})) })

		require.NoError(t, configure(t.Context(), []byte(`{"greeting":"hi"}`), plugin.Env{}))
		require.NoError(t, configure(t.Context(), nil, plugin.Env{}))
		assert.Empty(t, currentGreeting(), "an empty config block must clear a previous greeting")
	})

	t.Run("rejects invalid JSON", func(t *testing.T) {
		err := configure(t.Context(), []byte(`{`), plugin.Env{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode provider config")
	})
}
