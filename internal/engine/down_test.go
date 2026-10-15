package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/step"
)

func TestDownStep(t *testing.T) {
	newDownRun := func(t *testing.T, plugin stepPlugin, down bool) *run {
		t.Helper()
		r := newUpRun(t, plugin, newMockrouteTable(t), newMocktrafficRelay(t))
		r.caps = map[string]pluginhost.Info{"echo": {Steps: []pluginhost.StepInfo{{Name: "echo", Down: down}}}}
		return r
	}
	stateOf := func(r *run) session.State { return r.store.Snapshot().Steps[0].State }

	t.Run("sends the step's outputs to Down and marks it removed", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newDownRun(t, plugin, true)
		outputs := output.Map{"id": output.Value{String: "abc"}}
		plugin.EXPECT().Down(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, req step.DownRequest, _ step.Events) error {
				assert.Equal(t, "a", req.Step)
				assert.Equal(t, "echo", req.Type)
				assert.Equal(t, outputs, req.Outputs)
				return nil
			})

		_, err := r.downStep(t.Context(), "a", nil, map[string]output.Map{"a": outputs})

		require.NoError(t, err)
		assert.Equal(t, session.Removed, stateOf(r))
	})

	t.Run("a Down error fails the step and is returned", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newDownRun(t, plugin, true)
		plugin.EXPECT().Down(mock.Anything, mock.Anything, mock.Anything).Return(assert.AnError)

		_, err := r.downStep(t.Context(), "a", nil, nil)

		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, session.Failed, stateOf(r))
	})

	t.Run("a step type without Down is removed without calling the plugin", func(t *testing.T) {
		r := newDownRun(t, newMockstepPlugin(t), false)

		_, err := r.downStep(t.Context(), "a", nil, nil)

		require.NoError(t, err)
		assert.Equal(t, session.Removed, stateOf(r))
	})
}
