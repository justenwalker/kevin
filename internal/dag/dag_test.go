package dag

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		needs  map[string][]string
		expect error
	}{
		{
			name:  "linear",
			needs: map[string][]string{"a": nil, "b": {"a"}, "c": {"b"}},
		},
		{
			name:  "diamond",
			needs: map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}, "d": {"b", "c"}},
		},
		{
			name:   "unknown dependency",
			needs:  map[string][]string{"a": {"nope"}},
			expect: ErrUnknownStep,
		},
		{
			name:   "two node cycle",
			needs:  map[string][]string{"a": {"b"}, "b": {"a"}},
			expect: ErrCycle,
		},
		{
			name:   "self cycle",
			needs:  map[string][]string{"a": {"a"}},
			expect: ErrCycle,
		},
		{
			name:   "cycle with a clean tail",
			needs:  map[string][]string{"a": nil, "b": {"a", "d"}, "c": {"b"}, "d": {"c"}},
			expect: ErrCycle,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.needs).Validate()
			if tt.expect == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.expect)
		})
	}
}

func TestCycleErrorReportsTheStepsAndUnwrapsToErrCycle(t *testing.T) {
	err := New(map[string][]string{"a": {"b"}, "b": {"a"}}).Validate()
	require.Error(t, err)

	var cycleErr *CycleError
	require.ErrorAs(t, err, &cycleErr)
	assert.Equal(t, []string{"a", "b"}, cycleErr.Steps)
	assert.Equal(t, "dag: graph contains a cycle: [a b]", cycleErr.Error())
	assert.Equal(t, ErrCycle, errors.Unwrap(err))
}

func TestWalk(t *testing.T) {
	t.Run("orders a dependent after what it needs", func(t *testing.T) {
		// Step d waits on b and c. The two of them wait on a.
		g := New(map[string][]string{
			"a": nil,
			"b": {"a"},
			"c": {"a"},
			"d": {"b", "c"},
		})

		var mu sync.Mutex
		var order []string

		results, err := g.Walk(t.Context(), func(_ context.Context, name string, deps map[string]Outputs) (Outputs, error) {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return Outputs{"from": name, "deps": joinKeys(deps)}, nil
		})
		require.NoError(t, err)

		require.Len(t, order, 4)
		assert.Equal(t, "a", order[0], "a has no dependency, thus a must run first")
		assert.Equal(t, "d", order[3], "d depends on every other step, thus d must run last")

		assert.Equal(t, "b,c", results["d"]["deps"], "d must see the outputs of the two upstream steps")
		assert.Equal(t, "a", results["b"]["deps"])
	})

	t.Run("runs independent steps in parallel", func(t *testing.T) {
		g := New(map[string][]string{"a": nil, "b": nil, "c": nil})

		var inFlight, peak atomic.Int32
		_, err := g.Walk(t.Context(), func(context.Context, string, map[string]Outputs) (Outputs, error) {
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
			return nil, nil //nolint:nilnil // a nil Outputs is a valid empty result, not the caller ever mistaking it for "not found"
		})
		require.NoError(t, err)
		assert.Equal(t, int32(3), peak.Load(), "independent steps must overlap")
	})

	t.Run("a failure skips dependents and reports the root cause", func(t *testing.T) {
		g := New(map[string][]string{
			"a":         nil,
			"boom":      {"a"},
			"after":     {"boom"},
			"unrelated": nil,
		})

		var mu sync.Mutex
		ran := map[string]bool{}

		results, err := g.Walk(t.Context(), func(_ context.Context, name string, _ map[string]Outputs) (Outputs, error) {
			mu.Lock()
			ran[name] = true
			mu.Unlock()
			if name == "boom" {
				return nil, assert.AnError
			}
			return Outputs{"ok": name}, nil
		})

		require.ErrorIs(t, err, assert.AnError, "the root cause must survive, not a skip error")
		assert.Contains(t, err.Error(), `step "boom"`)

		assert.False(t, ran["after"], "a step whose dependency failed must not run")
		assert.NotContains(t, results, "boom")
		assert.NotContains(t, results, "after")
		assert.Contains(t, results, "a", "Walk must return the completed steps, so the caller can remove them")
	})

	t.Run("returns a validation error without running any step", func(t *testing.T) {
		g := New(map[string][]string{"a": {"nope"}})

		called := false
		_, err := g.Walk(t.Context(), func(context.Context, string, map[string]Outputs) (Outputs, error) {
			called = true
			return nil, nil //nolint:nilnil // a nil Outputs is a valid empty result, not the caller ever mistaking it for "not found"
		})

		require.ErrorIs(t, err, ErrUnknownStep)
		assert.False(t, called, "an invalid graph must not run any step")
	})
}

// TestWaitForDeps exercises the two branches that need a context canceled
// while waitForDeps is waiting. Walk's public API can't force that ordering
// deterministically - hitting them would race a step's own goroutine against
// the one under test - so this calls the unexported method directly with an
// already-canceled context instead.
func TestWaitForDeps(t *testing.T) {
	tests := []struct {
		name  string
		needs map[string][]string
		done  map[string]chan struct{}
	}{
		{
			name:  "stops waiting when the context is canceled",
			needs: map[string][]string{"b": {"a"}},
			done:  map[string]chan struct{}{"a": make(chan struct{})}, // never closes
		},
		{
			name:  "rechecks the context after every dep is done",
			needs: map[string][]string{"b": nil},
			done:  map[string]chan struct{}{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &Graph{needs: tt.needs}
			var mu sync.Mutex
			results := map[string]result{}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			deps, ok := g.waitForDeps(ctx, "b", tt.done, &mu, results)
			assert.False(t, ok)
			assert.Nil(t, deps)
		})
	}
}

func TestWalkFrom(t *testing.T) {
	t.Run("runs only the named steps and reuses prior outputs for the rest", func(t *testing.T) {
		g := New(map[string][]string{
			"a": nil,
			"b": {"a"},
			"c": {"b"},
		})

		var mu sync.Mutex
		var ran []string

		results, err := g.WalkFrom(t.Context(),
			map[string]bool{"c": true},
			map[string]Outputs{"a": {"v": "a"}, "b": {"v": "b"}},
			func(_ context.Context, name string, deps map[string]Outputs) (Outputs, error) {
				mu.Lock()
				ran = append(ran, name)
				mu.Unlock()
				return Outputs{"v": name, "saw": deps["b"]["v"]}, nil
			},
		)
		require.NoError(t, err)

		assert.Equal(t, []string{"c"}, ran, "a step not named in run must not call fn")
		assert.Equal(t, "b", results["c"]["saw"], "a dependent that IS in run must still see the prior output of a step that is not")
		assert.Equal(t, Outputs{"v": "a"}, results["a"], "a step outside run publishes its prior output unchanged")
		assert.Equal(t, Outputs{"v": "b"}, results["b"])
	})

	t.Run("skips a step with no prior output and not in run", func(t *testing.T) {
		g := New(map[string][]string{
			"a":    nil,
			"boom": {"a"},
			"next": {"boom"},
		})

		// "boom" is neither in run nor has a prior output - exactly what a
		// supervisor rerun of a step untouched by the original failure would
		// pass, since "boom" and its dependents never completed.
		results, err := g.WalkFrom(t.Context(),
			map[string]bool{"a": true},
			nil,
			func(_ context.Context, name string, _ map[string]Outputs) (Outputs, error) {
				return Outputs{"v": name}, nil
			},
		)
		require.NoError(t, err)

		assert.Contains(t, results, "a")
		assert.NotContains(t, results, "boom", "a step with no prior output and not in run must be skipped, not run")
		assert.NotContains(t, results, "next", "a dependent of a skipped step must be skipped too")
	})
}

func TestReverse(t *testing.T) {
	g := New(map[string][]string{"a": nil, "b": {"a"}, "c": {"b"}})

	var mu sync.Mutex
	var order []string

	_, err := g.Reverse().Walk(t.Context(), func(_ context.Context, name string, _ map[string]Outputs) (Outputs, error) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		return nil, nil //nolint:nilnil // a nil Outputs is a valid empty result, not the caller ever mistaking it for "not found"
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "b", "a"}, order, "a dependent step must be removed before its dependency")
}

func TestDependents(t *testing.T) {
	tree := map[string][]string{
		"base":      nil,
		"mid1":      {"base"},
		"mid2":      {"mid1"},
		"top":       {"mid2"},
		"unrelated": nil,
	}
	tests := []struct {
		name  string
		needs map[string][]string
		query string
		want  []string
	}{
		{
			name:  "returns every transitive dependent except self",
			needs: tree,
			query: "mid1", want: []string{"mid2", "top"},
		},
		{
			name:  "a step with no dependents returns an empty slice",
			needs: tree,
			query: "top",
		},
		{
			name:  "an unrelated step returns an empty slice",
			needs: tree,
			query: "unrelated",
		},
		{
			name: "visits a converging step only once",
			needs: map[string][]string{
				"base": nil,
				"mid1": {"base"},
				"mid2": {"base"},
				"top":  {"mid1", "mid2"},
			},
			query: "base",
			want:  []string{"mid1", "mid2", "top"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := New(tt.needs).Dependents(tt.query)
			if tt.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTopoSort(t *testing.T) {
	tests := []struct {
		name  string
		needs map[string][]string
		want  []string
	}{
		{
			name:  "orders a dependent after what it needs",
			needs: map[string][]string{"c": {"b"}, "b": {"a"}, "a": nil},
			want:  []string{"a", "b", "c"},
		},
		{
			// b and c both depend only on a, and neither depends on the
			// other: either order satisfies the DAG, but the result must
			// be the same every time.
			name:  "breaks ties alphabetically",
			needs: map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}},
			want:  []string{"a", "b", "c"},
		},
		{
			// c has no dependency on the cycle, so it still comes out; a
			// and b never do - Validate is what reports the cycle as an
			// error.
			name:  "stops at a cycle",
			needs: map[string][]string{"a": {"b"}, "b": {"a"}, "c": nil},
			want:  []string{"c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, New(tt.needs).TopoSort())
		})
	}
}

func joinKeys(m map[string]Outputs) string {
	keys := slices.Sorted(maps.Keys(m))
	return strings.Join(keys, ",")
}
