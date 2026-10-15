package kubernetes

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOrRollBack(t *testing.T) {
	ok := func(context.Context) ([]string, error) { return []string{"n1"}, nil }
	fail := func(context.Context) ([]string, error) { return []string{"n1"}, assert.AnError }

	t.Run("a successful create is not rolled back", func(t *testing.T) {
		rolled := false

		nodes, err := createOrRollBack(t.Context(), false, ok,
			func(context.Context) error { rolled = true; return nil })

		require.NoError(t, err)
		assert.Equal(t, []string{"n1"}, nodes)
		assert.False(t, rolled)
	})

	t.Run("a failed create is rolled back and returns the create error", func(t *testing.T) {
		rolled := false

		nodes, err := createOrRollBack(t.Context(), false, fail,
			func(context.Context) error { rolled = true; return context.Canceled })

		require.ErrorIs(t, err, assert.AnError)
		assert.Nil(t, nodes)
		assert.True(t, rolled)
	})

	t.Run("retain keeps what a failed create made", func(t *testing.T) {
		rolled := false

		_, err := createOrRollBack(t.Context(), true, fail,
			func(context.Context) error { rolled = true; return nil })

		require.ErrorIs(t, err, assert.AnError)
		assert.False(t, rolled)
	})

	t.Run("the rollback runs even when the caller's context is canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		var rollCtxErr error

		_, _ = createOrRollBack(ctx, false, fail,
			func(c context.Context) error { rollCtxErr = c.Err(); return nil })

		require.NoError(t, rollCtxErr)
	})
}
