// Package dag implements a Directed Acyclic [Graph] (DAG).
// This package implements the pure DAG algorithm.
// It also exposes methods to Walk and Sort the graph.
package dag

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// Outputs are the values that a node produces when walked.
// These outputs are made available to the node's dependents.
type Outputs map[string]any

// NodeFunc is run on each step in a graph Walk.
// This function is called on each node in the graph.
// The name of the node as well as the outputs of its dependencies are passed to the function.
// The [Outputs] returned will thus be made available to this node's dependents.
// Returning an error from a NodeFunc will prevent dependent nodes from being visited.
type NodeFunc func(ctx context.Context, name string, deps map[string]Outputs) (Outputs, error)

// Graph is a Directed Acyclic [Graph] (DAG).
type Graph struct {
	needs map[string][]string
}

// New builds a graph from a map. The key is a step name, and the value is the
// names that the step depends on. New copies the map, so the caller can reuse it.
func New(needs map[string][]string) *Graph {
	g := &Graph{needs: make(map[string][]string, len(needs))}
	for name, deps := range needs {
		g.needs[name] = append([]string(nil), deps...)
	}
	return g
}

// Steps return every step name, sorted in alphabetical order.
func (g *Graph) Steps() []string {
	names := make([]string, 0, len(g.needs))
	for name := range g.needs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TopoSort returns every step name in a dependency order: a step never
// precedes one it needs. Ties break alphabetically, so the result is
// deterministic. A cycle leaves the steps involved in it off the end of
// the result; Validate is what reports a cycle as an error.
func (g *Graph) TopoSort() []string {
	remaining := make(map[string]int, len(g.needs))
	for name, deps := range g.needs {
		remaining[name] = len(deps)
	}
	dependents := make(map[string][]string, len(g.needs))
	for name, deps := range g.needs {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}

	names := g.Steps() // alphabetical, fixed scan order
	done := make(map[string]bool, len(names))
	order := make([]string, 0, len(names))
	for len(order) < len(names) {
		progressed := false
		for _, name := range names {
			if done[name] || remaining[name] > 0 {
				continue
			}
			done[name] = true
			order = append(order, name)
			for _, dependent := range dependents[name] {
				remaining[dependent]--
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}
	return order
}

// Validate reports an unknown dependency and reports a cycle.
func (g *Graph) Validate() error {
	for _, name := range g.Steps() {
		for _, dep := range g.needs[name] {
			if _, ok := g.needs[dep]; !ok {
				return fmt.Errorf("dag: step %q needs %q: %w", name, dep, ErrUnknownStep)
			}
		}
	}
	return g.checkCycles()
}

// checkCycles runs the Kahn algorithm.
// It reports the steps that have no valid order.
func (g *Graph) checkCycles() error {
	remaining := make(map[string]int, len(g.needs))
	for name, deps := range g.needs {
		remaining[name] = len(deps)
	}
	// The dependents map lists, for each step, the steps that wait on it.
	dependents := make(map[string][]string, len(g.needs))
	for name, deps := range g.needs {
		for _, dep := range deps {
			dependents[dep] = append(dependents[dep], name)
		}
	}
	queue := make([]string, 0, len(remaining))
	for name, n := range remaining {
		if n == 0 {
			queue = append(queue, name)
		}
	}
	settled := 0
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		settled++
		for _, dependent := range dependents[name] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if settled != len(g.needs) {
		stuck := make([]string, 0, len(g.needs)-settled)
		for name, n := range remaining {
			if n > 0 {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		return &CycleError{
			Steps: stuck,
		}
	}
	return nil
}

// Reverse returns a graph with every edge inverted. A step in the new graph
// runs only after every step that depends on it is complete.
func (g *Graph) Reverse() *Graph {
	needs := make(map[string][]string, len(g.needs))
	for name := range g.needs {
		needs[name] = nil
	}
	for name, deps := range g.needs {
		for _, dep := range deps {
			needs[dep] = append(needs[dep], name)
		}
	}
	return &Graph{needs: needs}
}

// Dependents returns every step that transitively needs name - the steps
// that would be skipped if name failed. The result excludes name itself.
func (g *Graph) Dependents(name string) []string {
	direct := g.Reverse().needs
	seen := map[string]bool{name: true}
	queue := append([]string(nil), direct[name]...)
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		queue = append(queue, direct[next]...)
	}
	delete(seen, name)
	out := make([]string, 0, len(seen))
	for step := range seen {
		out = append(out, step)
	}
	sort.Strings(out)
	return out
}

// result records what happened to one step. A step that never ran keeps ran
// false and err nil.
type result struct {
	ran bool
	out Outputs
	err error
}

// Walk runs every step in dependency order. Steps that do not depend on each
// other run at the same time.
//
// The first step that fails cancels the context of the other steps. Walk skips
// a step when a dependency of that step fails, and reports the error of the
// step that failed.
//
// The returned map holds the outputs of every step that is complete, even when
// Walk returns an error.
func (g *Graph) Walk(ctx context.Context, fn NodeFunc) (map[string]Outputs, error) {
	return g.walk(ctx, fn, nil, nil)
}

// WalkFrom re-runs the steps named in run, calling fn for each. A step not
// named in run is treated as already complete: its recorded output, from
// prior, is published immediately for any dependent that IS in run. A step
// that is neither in run nor has an entry in prior is skipped, the same way
// Walk skips a dependent of a failed step.
func (g *Graph) WalkFrom(ctx context.Context, run map[string]bool, prior map[string]Outputs, fn NodeFunc) (map[string]Outputs, error) {
	return g.walk(ctx, fn, run, prior)
}

// waitForDeps blocks until every dependency of name is done, and collects
// their outputs. ok is false when ctx is canceled or a dependency did not
// run - the caller must then skip name without an error, the same way Walk
// skips a dependent of a failed step.
func (g *Graph) waitForDeps(ctx context.Context, name string, done map[string]chan struct{}, mu *sync.Mutex, resultsMap map[string]result) (map[string]Outputs, bool) {
	deps := make(map[string]Outputs, len(g.needs[name]))
	for _, dep := range g.needs[name] {
		select {
		case <-done[dep]:
		case <-ctx.Done():
			return nil, false
		}
		mu.Lock()
		up := resultsMap[dep]
		mu.Unlock()
		if !up.ran {
			return nil, false
		}
		deps[dep] = up.out
	}
	if err := ctx.Err(); err != nil {
		return nil, false
	}
	return deps, true
}

// walk implements both Walk and WalkFrom. only == nil means every step is in
// scope, the behavior Walk needs; otherwise a step not in only short-circuits
// to its prior output (or a skip, if it has none) instead of calling fn.
func (g *Graph) walk(ctx context.Context, fn NodeFunc, only map[string]bool, prior map[string]Outputs) (map[string]Outputs, error) {
	if err := g.Validate(); err != nil {
		return nil, err
	}

	done := make(map[string]chan struct{}, len(g.needs))
	for name := range g.needs {
		done[name] = make(chan struct{})
	}

	var mu sync.Mutex
	resultsMap := make(map[string]result, len(g.needs))

	finish := func(name string, o result) {
		mu.Lock()
		resultsMap[name] = o
		mu.Unlock()
		close(done[name])
	}

	grp, ctx := errgroup.WithContext(ctx)
	for _, name := range g.Steps() {
		grp.Go(func() error {
			if only != nil && !only[name] {
				out, ok := prior[name]
				finish(name, result{ran: ok, out: out})
				return nil
			}

			deps, ok := g.waitForDeps(ctx, name, done, &mu, resultsMap)
			if !ok {
				// Report no error here. The step that failed reports it,
				// and a cascade would hide the root cause.
				finish(name, result{})
				return nil
			}

			out, err := fn(ctx, name, deps)
			if err != nil {
				err = fmt.Errorf("dag: step %q: %w", name, err)
				finish(name, result{err: err})
				return err
			}
			finish(name, result{ran: true, out: out})
			return nil
		})
	}

	walkErr := grp.Wait()

	nodeOuputMap := make(map[string]Outputs, len(resultsMap))
	for name, o := range resultsMap {
		if o.ran {
			nodeOuputMap[name] = o.out
		}
	}
	return nodeOuputMap, walkErr //nolint:wrapcheck // each step's error is already wrapped with its own step name before grp.Wait sees it
}
