package session

import (
	"bytes"
	"sync"
)

const (
	// maxLines is the size of the "All" tab's tail.
	maxLines = 2000

	// maxRequests is the size of the Proxy tab's tail.
	maxRequests = 50
)

// Event is one change Store reports to its listener, if it has one - a
// Step, a Line, or a Request, depending on what changed.
type Event interface{ event() }

func (Step) event()    {}
func (Line) event()    {}
func (Request) event() {}

// Store holds the state of one session - the DAG's steps, their log
// output, and the proxy's request tail. The zero value is not usable.
// Call [NewStore]. A Store is safe for concurrent use.
type Store struct {
	mu        sync.RWMutex // guards the fields below
	steps     map[string]*Step
	order     []string // step names, in the order they were added
	lines     []Line   // the "All" tab's tail
	requests  []Request
	proxyAddr string
	onChange  func(Event)
}

// NewStore builds an empty Store.
func NewStore() *Store {
	return &Store{steps: map[string]*Step{}}
}

// OnChange registers fn to be called after every mutation, with the value
// that changed. Only one listener is needed today (the console's SSE
// push), so this replaces any previously registered fn rather than
// fanning out to several.
func (s *Store) OnChange(fn func(Event)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

func (s *Store) notify(e Event) {
	s.mu.RLock()
	fn := s.onChange
	s.mu.RUnlock()
	if fn != nil {
		fn(e)
	}
}

// SetProxyAddr records where the proxy listens, for a consumer to show.
func (s *Store) SetProxyAddr(addr string) {
	s.mu.Lock()
	s.proxyAddr = addr
	s.mu.Unlock()
}

// maxIconBytes bounds what AddStep will embed for a provider's icon -
// generous headroom over a small (48x48 or less) PNG.
const maxIconBytes = 64 * 1024

// pngMagic is the signature every PNG file starts with.
var pngMagic = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// validIcon returns icon unchanged, or nil when it's empty, oversized, or
// doesn't start with the PNG signature.
func validIcon(icon []byte) []byte {
	if len(icon) == 0 || len(icon) > maxIconBytes || !bytes.HasPrefix(icon, pngMagic) {
		return nil
	}
	return icon
}

// AddStep puts a step on before it runs. An empty label means a consumer
// shows name instead. provider names the plugin that backs the step, icon
// is that plugin's own PNG bytes, or nil for none, needs are the names of
// the steps it depends on, and compact marks a gate-like step the sidebar
// renders as a single muted line.
func (s *Store) AddStep(name, label, kind, provider string, icon []byte, needs []string, compact bool) {
	if label == "" {
		label = name
	}
	s.mu.Lock()
	if _, ok := s.steps[name]; !ok {
		s.steps[name] = &Step{
			Name: name, Label: label, State: Pending, Kind: kind, Provider: provider,
			Icon: validIcon(icon), Needs: append([]string(nil), needs...), Compact: compact,
		}
		s.order = append(s.order, name)
	}
	step := *s.steps[name]
	s.mu.Unlock()

	s.notify(step)
}

// SetStep moves a step to a new state. An unknown step is added.
func (s *Store) SetStep(name string, state State, message string) {
	s.mu.Lock()
	step, ok := s.steps[name]
	if !ok {
		step = &Step{Name: name, Label: name}
		s.steps[name] = step
		s.order = append(s.order, name)
	}
	step.State = state
	step.Message = message
	snapshot := *step
	s.mu.Unlock()

	s.notify(snapshot)
}

// AddStepDetail records one row for a step's card. A step can publish more
// than one detail, so AddStepDetail appends rather than overwrites.
func (s *Store) AddStepDetail(name string, d Detail) {
	s.mu.Lock()
	step, ok := s.steps[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	// A fresh backing array on every append: a snapshot already handed to
	// Snapshot() or a previous notify must never see itself mutated later.
	step.Details = append(append([]Detail(nil), step.Details...), d)
	snapshot := *step
	s.mu.Unlock()

	s.notify(snapshot)
}

// ClearStepDetails removes every detail row a step's card holds. An
// unknown step is a no-op.
func (s *Store) ClearStepDetails(name string) {
	s.mu.Lock()
	step, ok := s.steps[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	step.Details = nil
	snapshot := *step
	s.mu.Unlock()

	s.notify(snapshot)
}

// SetStepProgress records an estimated completion fraction for a step. An
// unknown step is a no-op.
func (s *Store) SetStepProgress(name string, fraction float64) {
	s.mu.Lock()
	step, ok := s.steps[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	step.Progress = fraction
	snapshot := *step
	s.mu.Unlock()

	s.notify(snapshot)
}

// SetStepIdempotent records whether a step's type is safe to call Up on
// again. An unknown step is a no-op.
func (s *Store) SetStepIdempotent(name string, idempotent bool) {
	s.mu.Lock()
	step, ok := s.steps[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	step.Idempotent = idempotent
	snapshot := *step
	s.mu.Unlock()

	s.notify(snapshot)
}

// Log records one line of output from a step, both in the shared "All"
// tail and in that step's own tail.
func (s *Store) Log(step, stream, text string) {
	line := Line{Step: step, Stream: stream, Text: text}

	s.mu.Lock()
	s.lines = append(s.lines, line)
	if len(s.lines) > maxLines {
		s.lines = s.lines[len(s.lines)-maxLines:]
	}
	s.mu.Unlock()

	s.notify(line)
}

// Record adds one request that passed through the proxy. The caller sets
// r.Time; Record does not stamp it.
func (s *Store) Record(r Request) {
	s.mu.Lock()
	s.requests = append([]Request{r}, s.requests...)
	if len(s.requests) > maxRequests {
		s.requests = s.requests[:maxRequests]
	}
	s.mu.Unlock()

	s.notify(r)
}

// Snapshot returns the current state.
func (s *Store) Snapshot() View {
	s.mu.RLock()
	defer s.mu.RUnlock()

	steps := make([]Step, 0, len(s.order))
	for _, name := range s.order {
		steps = append(steps, *s.steps[name])
	}
	// A step's own tail is the "All" buffer filtered by name, not a
	// separately maintained copy - maxLines already bounds the source, so
	// this filter is cheap and runs only on a snapshot, not per event.
	stepLogs := make(map[string][]Line, len(steps))
	for _, l := range s.lines {
		stepLogs[l.Step] = append(stepLogs[l.Step], l)
	}
	return View{
		ProxyAddr: s.proxyAddr,
		Steps:     steps,
		Logs:      append([]Line(nil), s.lines...),
		StepLogs:  stepLogs,
		Requests:  append([]Request(nil), s.requests...),
	}
}
