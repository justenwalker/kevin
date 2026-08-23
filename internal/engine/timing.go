package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// TimingsFile holds the timing history of a project, relative to the
// workspace.
const TimingsFile = "timings.json"

// maxSamples bounds the history kept for one key, so the file stays small
// and one old outlier ages out.
const maxSamples = 5

// timingsFile is the on-disk shape of TimingsFile.
type timingsFile struct {
	// Steps keys a sample list by step name.
	Steps map[string]samples `json:"steps,omitempty"`
	// Types keys a sample list by step type, "<plugin>:<step>". It serves an
	// estimate for a step name with no history of its own.
	Types map[string]samples `json:"types,omitempty"`
}

// samples holds the last few durations of one key, in milliseconds, oldest
// first.
type samples struct {
	Up   []int64 `json:"up,omitempty"`
	Down []int64 `json:"down,omitempty"`
}

// timingStore is the in-memory, mutex-guarded view of TimingsFile. A nil
// *timingStore is valid: every method no-ops, so a caller that builds a run
// without loading timings (as tests do) needs no nil check of its own.
type timingStore struct {
	mu   sync.Mutex
	path string
	data timingsFile
}

// loadTimings reads path. A missing file is the normal first run and yields
// an empty store. A corrupt file is logged and also yields an empty store -
// timing history is an estimate, not state worth failing a run over.
func loadTimings(ctx context.Context, path string) *timingStore {
	s := &timingStore{path: path}

	b, err := os.ReadFile(path) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return s
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		log.Ctx(ctx).Warn("discarding corrupt timings file", "path", path, "error", err)
		s.data = timingsFile{}
	}
	return s
}

// writeAtomic writes b to path with a temp-file rename, so a crash mid-write
// leaves the previous file intact rather than a half-written one that reads
// as corrupt forever after. writeAtomic is best-effort: a failure is logged,
// not returned, since losing timing history costs nothing but an estimate.
func writeAtomic(ctx context.Context, path string, b []byte) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Ctx(ctx).Warn("save timings", "path", path, "error", err)
		return
	}

	tmp, err := os.CreateTemp(dir, ".timings-*.tmp")
	if err != nil {
		log.Ctx(ctx).Warn("save timings", "path", path, "error", err)
		return
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		log.Ctx(ctx).Warn("save timings", "path", path, "error", err)
		return
	}
	if err := tmp.Close(); err != nil {
		log.Ctx(ctx).Warn("save timings", "path", path, "error", err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		log.Ctx(ctx).Warn("save timings", "path", path, "error", err)
	}
}

// EstimateUp reports a duration to expect for step name's Up, from its own
// history, or failing that from the history of every step of type uses. ok
// is false when neither exists.
func (s *timingStore) EstimateUp(name, uses string) (time.Duration, bool) {
	return s.estimate(name, uses, func(x samples) []int64 { return x.Up })
}

// EstimateDown is EstimateUp for a step's Down.
func (s *timingStore) EstimateDown(name, uses string) (time.Duration, bool) {
	return s.estimate(name, uses, func(x samples) []int64 { return x.Down })
}

func (s *timingStore) estimate(name, uses string, pick func(samples) []int64) (time.Duration, bool) {
	if s == nil {
		return 0, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if ms := pick(s.data.Steps[name]); len(ms) > 0 {
		return median(ms), true
	}
	if ms := pick(s.data.Types[uses]); len(ms) > 0 {
		return median(ms), true
	}
	return 0, false
}

// RecordUp adds a completed Up duration for step name of type uses, under
// both keys, and persists the history.
func (s *timingStore) RecordUp(ctx context.Context, name, uses string, d time.Duration) {
	s.record(ctx, name, uses, d, func(x *samples, ms int64) { x.Up = pushSample(x.Up, ms) })
}

// RecordDown is RecordUp for a step's Down.
func (s *timingStore) RecordDown(ctx context.Context, name, uses string, d time.Duration) {
	s.record(ctx, name, uses, d, func(x *samples, ms int64) { x.Down = pushSample(x.Down, ms) })
}

func (s *timingStore) record(ctx context.Context, name, uses string, d time.Duration, push func(*samples, int64)) {
	if s == nil {
		return
	}

	ms := d.Milliseconds()

	s.mu.Lock()
	if s.data.Steps == nil {
		s.data.Steps = map[string]samples{}
	}
	if s.data.Types == nil {
		s.data.Types = map[string]samples{}
	}
	step := s.data.Steps[name]
	push(&step, ms)
	s.data.Steps[name] = step

	typ := s.data.Types[uses]
	push(&typ, ms)
	s.data.Types[uses] = typ

	// Marshal while still holding the lock: s.data is only safe to read
	// under s.mu, but the file write itself needs no lock once b is a
	// standalone copy.
	b, err := json.Marshal(s.data)
	s.mu.Unlock()
	if err != nil {
		log.Ctx(ctx).Warn("save timings", "path", s.path, "error", err)
		return
	}

	writeAtomic(ctx, s.path, b)
}

// pushSample appends v, dropping the oldest sample once the list holds more
// than maxSamples.
func pushSample(list []int64, v int64) []int64 {
	list = append(list, v)
	if len(list) > maxSamples {
		list = list[len(list)-maxSamples:]
	}
	return list
}

// median returns the middle value of ms, or the average of the two middle
// values when ms has an even length. median does not mutate ms.
func median(ms []int64) time.Duration {
	sorted := append([]int64(nil), ms...)
	slices.Sort(sorted)

	n := len(sorted)
	mid := n / 2
	if n%2 == 1 {
		return time.Duration(sorted[mid]) * time.Millisecond
	}
	return time.Duration(sorted[mid-1]+sorted[mid]) * time.Millisecond / 2
}
