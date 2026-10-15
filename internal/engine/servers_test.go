package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServingClose(t *testing.T) {
	t.Run("stops once and reports the same result every call", func(t *testing.T) {
		stops := 0
		done := make(chan error, 1)
		done <- assert.AnError
		s := &serving{stop: context.CancelFunc(func() { stops++ }), done: done}

		first := s.Close()
		second := s.Close()

		require.ErrorIs(t, first, assert.AnError)
		require.ErrorIs(t, second, assert.AnError)
		assert.Equal(t, 1, stops)
	})
}
