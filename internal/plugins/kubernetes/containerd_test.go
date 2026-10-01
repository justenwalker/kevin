package kubernetes

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWaitContainerdReady(t *testing.T) {
	t.Run("ready on the first check", func(t *testing.T) {
		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "ctr github.com/containerd/containerd 1.7.0", nil
		}}
		require.NoError(t, waitContainerdReady(t.Context(), rt, "demo-control-plane"))
	})

	t.Run("a canceled context stops the poll instead of waiting out the timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		rt := fakeRuntime{exec: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("containerd not ready")
		}}
		err := waitContainerdReady(ctx, rt, "demo-control-plane")
		require.ErrorIs(t, err, context.Canceled)
	})
}
