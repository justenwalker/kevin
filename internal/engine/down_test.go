package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/plugins/launcher"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/step"
)

func TestDownStep(t *testing.T) {
	newDownRun := func(t *testing.T, plugin stepPlugin, routes routeTable, down bool) *run {
		t.Helper()
		r := newUpRun(t, plugin, routes, newMocktrafficRelay(t))
		r.caps = map[string]launcher.Info{"echo": {Steps: []launcher.StepInfo{{Name: "echo", Down: down}}}}
		return r
	}
	stateOf := func(r *run) session.State { return r.store.Snapshot().Steps[0].State }

	t.Run("sends the step's outputs to Down and marks it removed", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		routes := newMockrouteTable(t)
		r := newDownRun(t, plugin, routes, true)
		routes.EXPECT().ClearStep("a").Return()
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

	t.Run("a Down error fails the step, keeps its routes and is returned", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		r := newDownRun(t, plugin, newMockrouteTable(t), true)
		plugin.EXPECT().Down(mock.Anything, mock.Anything, mock.Anything).Return(assert.AnError)

		_, err := r.downStep(t.Context(), "a", nil, nil)

		require.ErrorIs(t, err, assert.AnError)
		assert.Equal(t, session.Failed, stateOf(r))
	})

	t.Run("a step type without Down is removed without calling the plugin", func(t *testing.T) {
		routes := newMockrouteTable(t)
		r := newDownRun(t, newMockstepPlugin(t), routes, false)
		routes.EXPECT().ClearStep("a").Return()

		_, err := r.downStep(t.Context(), "a", nil, nil)

		require.NoError(t, err)
		assert.Equal(t, session.Removed, stateOf(r))
	})
}
