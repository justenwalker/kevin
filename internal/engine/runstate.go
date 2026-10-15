package engine

import (
	"maps"
	"sync"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/step"
)

// runState is what a run has learned about each step so far, keyed by step
// name. The zero value is ready to use and it is safe for concurrent use:
// r.up and a rerun from the console run at the same time.
type runState struct {
	mu sync.Mutex

	// completed holds the outputs a step published, and containers the
	// containers it published for a dependent's UpRequest.Containers.
	completed  map[string]output.Map
	containers map[string][]step.Container

	// started holds the steps that called Up and have not failed on their
	// own since.
	started map[string]struct{}

	// system holds kevin-computed values (expose_<name>/forward_<name> for
	// an ExposedPort), kept apart from a step's own outputs so the two never
	// collide. They are read into the "system" CEL variable.
	system map[string]output.Map

	// faults holds the fault IDs a step's most recent Up applied through the
	// relay, so downStep can clear them even though DownRequest carries no
	// Result.
	faults map[string][]string

	// intercepting holds the steps whose latest Up registered an intercept
	// with the relay, so a rerun that drops it, or a step that goes away,
	// knows to release it.
	intercepting map[string]bool

	forwards map[string][]forward

	// locks holds one mutex per step. upStep holds a step's lock for its
	// whole call and rejects with ErrStepBusy rather than wait when it is
	// taken, so a rerun of a step still Pending in the initial walk, or
	// already mid-rerun, can never run Up twice at once.
	locks map[string]*sync.Mutex
}

// TryLock takes name's lock without waiting and returns the func that
// releases it. It reports false, with a nil func, when another call holds it.
func (s *runState) TryLock(name string) (func(), bool) {
	s.mu.Lock()
	if s.locks == nil {
		s.locks = map[string]*sync.Mutex{}
	}
	l, found := s.locks[name]
	if !found {
		l = &sync.Mutex{}
		s.locks[name] = l
	}
	s.mu.Unlock()
	if !l.TryLock() {
		return nil, false
	}
	return l.Unlock, true
}

// Complete records every step in results as complete.
func (s *runState) Complete(results map[string]output.Map) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completed == nil {
		s.completed = make(map[string]output.Map, len(results))
	}
	maps.Copy(s.completed, results)
}

// Snapshot returns a copy of the completed outputs.
func (s *runState) Snapshot() map[string]output.Map {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]output.Map, len(s.completed))
	maps.Copy(out, s.completed)
	return out
}

// MarkStarted records that name called Up.
func (s *runState) MarkStarted(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started == nil {
		s.started = make(map[string]struct{})
	}
	s.started[name] = struct{}{}
}

// MarkSettled records that name's Up failed on its own, which leaves any
// cleanup to the plugin.
func (s *runState) MarkSettled(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.started, name)
}

// Interrupted returns the steps that called Up and never completed, because
// the run was canceled while Up ran, or because a later stage of the step
// failed after Up succeeded.
func (s *runState) Interrupted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for name := range s.started {
		if _, done := s.completed[name]; !done {
			names = append(names, name)
		}
	}
	return names
}

// Containers returns the containers name last published, or nil if it never
// published any.
func (s *runState) Containers(name string) []step.Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.containers[name]
}

// SetContainers records name's published containers, replacing whatever an
// earlier Up recorded for it.
func (s *runState) SetContainers(name string, containers []step.Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(containers) == 0 {
		delete(s.containers, name)
		return
	}
	if s.containers == nil {
		s.containers = make(map[string][]step.Container)
	}
	s.containers[name] = containers
}

// SetSystem records name's kevin-computed values.
func (s *runState) SetSystem(name string, values output.Map) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.system == nil {
		s.system = make(map[string]output.Map)
	}
	s.system[name] = values
}

// System returns the recorded kevin-computed values of each step in deps.
func (s *runState) System(deps map[string]output.Map) map[string]output.Map {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]output.Map, len(deps))
	for dep := range deps {
		if values, ok := s.system[dep]; ok {
			out[dep] = values
		}
	}
	return out
}

// SetFaults replaces name's applied fault IDs. A later Up fully supersedes
// an earlier one, mirroring how ApplyFault replaces rather than stacks.
func (s *runState) SetFaults(name string, ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		delete(s.faults, name)
		return
	}
	if s.faults == nil {
		s.faults = make(map[string][]string)
	}
	s.faults[name] = ids
}

// TakeFaults returns name's applied fault IDs and forgets them.
func (s *runState) TakeFaults(name string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.faults[name]
	delete(s.faults, name)
	return ids
}

// SetIntercepting records whether name has intercepts registered with the
// relay.
func (s *runState) SetIntercepting(name string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !on {
		delete(s.intercepting, name)
		return
	}
	if s.intercepting == nil {
		s.intercepting = make(map[string]bool)
	}
	s.intercepting[name] = true
}

// Intercepting reports whether name has intercepts registered with the
// relay.
func (s *runState) Intercepting(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intercepting[name]
}

// AddForward records f under step.
func (s *runState) AddForward(step string, f forward) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.forwards == nil {
		s.forwards = make(map[string][]forward)
	}
	s.forwards[step] = append(s.forwards[step], f)
}

// TakeForwards returns step's forwards and forgets them.
func (s *runState) TakeForwards(step string) []forward {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs := s.forwards[step]
	delete(s.forwards, step)
	return fs
}

// TakeAllForwards returns every step's forwards and forgets them.
func (s *runState) TakeAllForwards() map[string][]forward {
	s.mu.Lock()
	defer s.mu.Unlock()
	fs := s.forwards
	s.forwards = nil
	return fs
}
