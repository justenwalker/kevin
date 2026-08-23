package engine

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventsWriter(t *testing.T) {
	t.Run("prefers a caller-supplied writer", func(t *testing.T) {
		w := &os.File{}
		assert.Same(t, w, eventsWriter(Options{Events: w}, true),
			"a caller-supplied writer wins even when termui would otherwise own the terminal")
		assert.Same(t, w, eventsWriter(Options{Events: w}, false))
	})

	t.Run("discards when live and unset", func(t *testing.T) {
		assert.Equal(t, io.Discard, eventsWriter(Options{}, true),
			"termui's live block already shows this information")
	})

	t.Run("falls back to stderr when not live", func(t *testing.T) {
		assert.Equal(t, os.Stderr, eventsWriter(Options{}, false))
	})
}
