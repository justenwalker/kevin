package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMedian(t *testing.T) {
	assert.Equal(t, 20*time.Millisecond, median([]int64{20}))
	assert.Equal(t, 20*time.Millisecond, median([]int64{10, 20, 30}))
	assert.Equal(t, 25*time.Millisecond, median([]int64{10, 20, 30, 40}))
}

func TestPushSampleCapsAndDropsOldest(t *testing.T) {
	var list []int64
	for i := int64(1); i <= 7; i++ {
		list = pushSample(list, i)
	}
	assert.Equal(t, []int64{3, 4, 5, 6, 7}, list)
}

func TestTimingStore(t *testing.T) {
	t.Run("round-trips a recorded estimate through the file", func(t *testing.T) {
		ctx := t.Context()
		path := filepath.Join(t.TempDir(), "timings.json")

		s := loadTimings(ctx, path)
		_, ok := s.EstimateUp("web", "builtin:container")
		assert.False(t, ok)

		s.RecordUp(ctx, "web", "builtin:container", 10*time.Second)
		s.RecordUp(ctx, "web", "builtin:container", 20*time.Second)

		got, ok := s.EstimateUp("web", "builtin:container")
		require.True(t, ok)
		assert.Equal(t, 15*time.Second, got)

		reloaded := loadTimings(ctx, path)
		got, ok = reloaded.EstimateUp("web", "builtin:container")
		require.True(t, ok)
		assert.Equal(t, 15*time.Second, got)
	})

	t.Run("falls back to the step type when the step has no estimate of its own", func(t *testing.T) {
		ctx := t.Context()
		s := loadTimings(ctx, filepath.Join(t.TempDir(), "timings.json"))

		s.RecordUp(ctx, "db", "builtin:container", 5*time.Second)

		got, ok := s.EstimateUp("api", "builtin:container")
		require.True(t, ok)
		assert.Equal(t, 5*time.Second, got)
	})

	t.Run("a corrupt file degrades to an empty store", func(t *testing.T) {
		ctx := t.Context()
		path := filepath.Join(t.TempDir(), "timings.json")
		require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

		s := loadTimings(ctx, path)
		_, ok := s.EstimateUp("web", "builtin:container")
		assert.False(t, ok)
	})

	t.Run("a nil store no-ops instead of panicking", func(t *testing.T) {
		var s *timingStore

		_, ok := s.EstimateUp("web", "builtin:container")
		assert.False(t, ok)
		_, ok = s.EstimateDown("web", "builtin:container")
		assert.False(t, ok)

		assert.NotPanics(t, func() {
			s.RecordUp(t.Context(), "web", "builtin:container", time.Second)
			s.RecordDown(t.Context(), "web", "builtin:container", time.Second)
		})
	})
}
