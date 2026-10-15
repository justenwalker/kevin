package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/step"
)

func TestRunStateTryLock(t *testing.T) {
	t.Run("a held lock refuses a second caller", func(t *testing.T) {
		var s runState
		unlock, ok := s.TryLock("a")
		require.True(t, ok)

		_, again := s.TryLock("a")
		assert.False(t, again)

		unlock()
		unlock, ok = s.TryLock("a")
		assert.True(t, ok)
		unlock()
	})

	t.Run("locks are per step", func(t *testing.T) {
		var s runState
		unlockA, ok := s.TryLock("a")
		require.True(t, ok)
		defer unlockA()

		unlockB, ok := s.TryLock("b")
		assert.True(t, ok)
		unlockB()
	})
}

func TestRunStateComplete(t *testing.T) {
	t.Run("a snapshot is a copy", func(t *testing.T) {
		var s runState
		s.Complete(map[string]output.Map{"a": {"k": {String: "v"}}})

		snap := s.Snapshot()
		snap["b"] = nil

		assert.Equal(t, map[string]output.Map{"a": {"k": {String: "v"}}}, s.Snapshot())
	})
}

func TestRunStateFaults(t *testing.T) {
	t.Run("SetFaults replaces a prior entry, not accumulates", func(t *testing.T) {
		var s runState
		s.SetFaults("fault", []string{"fault"})
		s.SetFaults("fault", []string{"fault-v2"})

		assert.Equal(t, []string{"fault-v2"}, s.TakeFaults("fault"))
	})

	t.Run("SetFaults with no ids clears the entry", func(t *testing.T) {
		var s runState
		s.SetFaults("fault", []string{"fault"})
		s.SetFaults("fault", nil)

		assert.Empty(t, s.TakeFaults("fault"))
	})

	t.Run("TakeFaults forgets what it returns", func(t *testing.T) {
		var s runState
		s.SetFaults("fault", []string{"f1"})

		s.TakeFaults("fault")

		assert.Empty(t, s.TakeFaults("fault"))
	})
}

func TestRunStateContainers(t *testing.T) {
	t.Run("SetContainers replaces, and an empty set forgets", func(t *testing.T) {
		var s runState
		s.SetContainers("a", []step.Container{{ID: "1"}})
		s.SetContainers("a", []step.Container{{ID: "2"}})
		require.Len(t, s.Containers("a"), 1)
		assert.Equal(t, "2", s.Containers("a")[0].ID)

		s.SetContainers("a", nil)

		assert.Nil(t, s.Containers("a"))
	})
}

func TestRunStateSystem(t *testing.T) {
	t.Run("System returns only the requested steps", func(t *testing.T) {
		var s runState
		s.SetSystem("a", output.Map{"expose_db": {String: "x"}})
		s.SetSystem("b", output.Map{"expose_db": {String: "y"}})

		got := s.System(map[string]output.Map{"a": nil, "c": nil})

		assert.Equal(t, map[string]output.Map{"a": {"expose_db": {String: "x"}}}, got)
	})
}

func TestRunStateInterrupted(t *testing.T) {
	t.Run("a started step that never completed is interrupted", func(t *testing.T) {
		var s runState
		s.MarkStarted("a")
		s.MarkStarted("b")
		s.Complete(map[string]output.Map{"b": nil})

		assert.Equal(t, []string{"a"}, s.Interrupted())
	})

	t.Run("a settled step is not interrupted", func(t *testing.T) {
		var s runState
		s.MarkStarted("a")
		s.MarkSettled("a")

		assert.Empty(t, s.Interrupted())
	})
}

func TestRunStateForwards(t *testing.T) {
	t.Run("TakeForwards removes only that step", func(t *testing.T) {
		var s runState
		web, db := &fakeForward{}, &fakeForward{}
		s.AddForward("web", web)
		s.AddForward("db", db)

		assert.Equal(t, []forward{web}, s.TakeForwards("web"))
		assert.Empty(t, s.TakeForwards("web"))
		assert.Equal(t, map[string][]forward{"db": {db}}, s.TakeAllForwards())
		assert.Empty(t, s.TakeAllForwards())
	})
}
