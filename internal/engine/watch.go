package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/internal/watch"
)

const (
	watchDebounce  = 300 * time.Millisecond
	watchBusyRetry = time.Second
)

// startWatch reruns each step with a watch list when files under it change.
// It watches nothing when noWait is set, since Run returns right after
// bring-up. The returned stop function ends the loop and waits for a rerun
// in flight.
func (r *run) startWatch(ctx context.Context, noWait bool) func() {
	paths := make(map[string][]string)
	for name, step := range r.steps {
		if len(step.Watch) > 0 {
			paths[name] = step.Watch
		}
	}
	if noWait || len(paths) == 0 {
		return func() {}
	}
	w, err := watch.New(r.cfg.Dir, paths, watchDebounce)
	if err != nil {
		r.emit("watch", "disabled: "+err.Error())
		return func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = w.Close() }()
		r.watchLoop(ctx, w.Events())
	}()
	return func() {
		cancel()
		<-done
	}
}

// rerunDone reports a finished watch-triggered rerun to watchLoop.
type rerunDone struct {
	step string
	busy bool
}

// watchLoop runs one rerun per step at a time. A change to a step that is
// still rerunning sets a dirty flag instead of queueing, so a burst of saves
// during one rerun causes one follow-up rerun.
func (r *run) watchLoop(ctx context.Context, events <-chan watch.Event) {
	running := make(map[string]string) // step -> path that triggered it
	dirty := make(map[string]string)
	finished := make(chan rerunDone)
	retry := make(chan watch.Event)

	var wg sync.WaitGroup
	defer wg.Wait()

	handle := func(ev watch.Event) {
		if _, ok := running[ev.Step]; ok {
			dirty[ev.Step] = ev.Path
			return
		}
		running[ev.Step] = ev.Path
		r.emit(ev.Step, "change in "+ev.Path+", rerunning")
		wg.Go(func() {
			err := r.RerunStep(ctx, ev.Step, true)
			select {
			case finished <- rerunDone{step: ev.Step, busy: errors.Is(err, session.ErrStepBusy)}:
			case <-ctx.Done():
			}
		})
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			handle(ev)
		case ev := <-retry:
			handle(ev)
		case d := <-finished:
			path, again := dirty[d.step]
			if !again {
				path = running[d.step]
			}
			delete(running, d.step)
			delete(dirty, d.step)
			switch {
			case d.busy:
				// Another rerun holds the step, so retry shortly instead of
				// waiting on its lock.
				ev := watch.Event{Step: d.step, Path: path}
				time.AfterFunc(watchBusyRetry, func() {
					select {
					case retry <- ev:
					case <-ctx.Done():
					}
				})
			case again:
				handle(watch.Event{Step: d.step, Path: path})
			}
		}
	}
}
