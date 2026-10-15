package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

// scopeRuntime is a projectRuntime double whose ListByLabel reports a fixed
// set of containers for the other scope.
type scopeRuntime struct {
	stubRuntime

	otherLive []string
	err       error
}

func (f *scopeRuntime) ListByLabel(context.Context, string, string) ([]string, error) {
	return f.otherLive, f.err
}

func TestStopRelayIfAlone(t *testing.T) {
	newRun := func(rt projectRuntime) *run {
		return &run{cfg: &config.Config{Project: "p"}, runtime: rt, scope: config.ScopeEnv}
	}

	t.Run("closes the relay when the other scope is not live", func(t *testing.T) {
		rl := newMocktrafficRelay(t)
		rl.EXPECT().Close().Return(nil)

		require.NoError(t, newRun(&scopeRuntime{}).stopRelayIfAlone(t.Context(), rl))
	})

	t.Run("leaves the relay when the other scope is live", func(t *testing.T) {
		rl := newMocktrafficRelay(t)

		require.NoError(t, newRun(&scopeRuntime{otherLive: []string{"setup-step"}}).stopRelayIfAlone(t.Context(), rl))
	})

	t.Run("leaves the relay when liveness cannot be read", func(t *testing.T) {
		rl := newMocktrafficRelay(t)

		err := newRun(&scopeRuntime{err: assert.AnError}).stopRelayIfAlone(t.Context(), rl)

		require.ErrorIs(t, err, assert.AnError)
	})
}
