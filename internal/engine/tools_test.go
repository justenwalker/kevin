package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestCallTool(t *testing.T) {
	newRun := func() *run {
		return &run{
			toolRoutes: map[string]toolRoute{"echo_echo_echo": {plugin: "echo", step: "echo", tool: "echo"}},
			steps: map[string]config.Step{
				"a": {Uses: config.StepRef{Plugin: "echo", Step: "echo"}},
				"w": {Uses: config.StepRef{Plugin: "builtin", Step: "wait"}},
			},
		}
	}

	t.Run("an unknown tool", func(t *testing.T) {
		_, err := newRun().CallTool(t.Context(), "a", "nope_nope_nope", nil)

		require.ErrorContains(t, err, `no such tool "nope_nope_nope"`)
	})

	t.Run("an unknown step", func(t *testing.T) {
		_, err := newRun().CallTool(t.Context(), "missing", "echo_echo_echo", nil)

		require.ErrorContains(t, err, `no step named "missing"`)
	})

	t.Run("a step of another type than the tool's owner", func(t *testing.T) {
		_, err := newRun().CallTool(t.Context(), "w", "echo_echo_echo", nil)

		require.ErrorContains(t, err, `step "w" is a builtin:wait step, not a echo:echo step`)
	})
}
