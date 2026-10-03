// Package watch reports which steps have changed files under their watched
// paths, debouncing bursts of writes into one event per step.
package watch

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Event reports that Path changed under one of Step's watched paths.
type Event struct {
	Step string
	Path string // relative to the project directory
}

// Watcher watches a set of paths per step. Create one with [New].
type Watcher struct {
	root     string
	debounce time.Duration
	fsw      *fsnotify.Watcher
	steps    map[string][]string // step -> absolute watched paths
	events   chan Event
	done     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	mu      sync.Mutex
	pending map[string]*pendingEvent
}

type pendingEvent struct {
	timer *time.Timer
	path  string
}

// New watches each step's paths, given relative to root. A directory is
// watched recursively. A burst of changes to one step's paths sends one
// [Event] once debounce passes with no further change.
func New(root string, steps map[string][]string, debounce time.Duration) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch: %w", err)
	}
	w := &Watcher{
		root:     root,
		debounce: debounce,
		fsw:      fsw,
		steps:    make(map[string][]string, len(steps)),
		events:   make(chan Event),
		done:     make(chan struct{}),
		pending:  make(map[string]*pendingEvent),
	}
	for step, paths := range steps {
		for _, p := range paths {
			abs := filepath.Join(root, p)
			w.steps[step] = append(w.steps[step], abs)
			if err := w.add(abs); err != nil {
				_ = fsw.Close()
				return nil, fmt.Errorf("watch: step %s: %w", step, err)
			}
		}
	}
	w.wg.Add(1)
	go w.loop()
	return w, nil
}

// Events returns the channel [Event]s arrive on. It is never closed, so stop
// reading it when you call [Watcher.Close].
func (w *Watcher) Events() <-chan Event { return w.events }

// Close stops watching and drops any pending event.
func (w *Watcher) Close() error {
	var err error
	w.stopOnce.Do(func() {
		close(w.done)
		if closeErr := w.fsw.Close(); closeErr != nil {
			err = fmt.Errorf("watch: close: %w", closeErr)
		}
		w.wg.Wait()
		w.mu.Lock()
		for _, p := range w.pending {
			p.timer.Stop()
		}
		w.mu.Unlock()
	})
	return err
}

// add watches path: a directory and every directory below it, or, for a
// file, its parent directory so an editor's save-by-rename is still seen.
func (w *Watcher) add(path string) error {
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if p == path {
				return w.fsw.Add(filepath.Dir(p))
			}
			return nil
		}
		if w.ignored(p) {
			return filepath.SkipDir
		}
		return w.fsw.Add(p)
	})
	if err != nil {
		return fmt.Errorf("add %s: %w", path, err)
	}
	return nil
}

func (w *Watcher) loop() {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) handle(ev fsnotify.Event) {
	if ev.Op == fsnotify.Chmod || w.ignored(ev.Name) {
		return
	}
	var matched []string
	for step, paths := range w.steps {
		for _, p := range paths {
			if within(p, ev.Name) {
				matched = append(matched, step)
				break
			}
		}
	}
	if len(matched) == 0 {
		return
	}
	if ev.Has(fsnotify.Create) {
		// A directory created after startup needs its own watch. The walk
		// fails harmlessly if it was removed again already.
		_ = w.add(ev.Name)
	}
	rel, err := filepath.Rel(w.root, ev.Name)
	if err != nil {
		rel = ev.Name
	}
	for _, step := range matched {
		w.schedule(step, rel)
	}
}

func (w *Watcher) schedule(step, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if p, ok := w.pending[step]; ok {
		p.path = path
		p.timer.Reset(w.debounce)
		return
	}
	p := &pendingEvent{path: path}
	p.timer = time.AfterFunc(w.debounce, func() { w.fire(step) })
	w.pending[step] = p
}

func (w *Watcher) fire(step string) {
	w.mu.Lock()
	p := w.pending[step]
	delete(w.pending, step)
	w.mu.Unlock()
	if p == nil {
		return
	}
	select {
	case w.events <- Event{Step: step, Path: p.path}:
	case <-w.done:
	}
}

// ignored reports whether path is kevin or git state, or an editor temp file.
func (w *Watcher) ignored(path string) bool {
	if rel, err := filepath.Rel(w.root, path); err == nil {
		for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
			if part == ".kevin" || part == ".git" {
				return true
			}
		}
	}
	base := filepath.Base(path)
	return strings.HasSuffix(base, "~") || strings.HasPrefix(base, ".#") || strings.HasSuffix(base, ".swp")
}

// within reports whether path is root or lies below it.
func within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}
