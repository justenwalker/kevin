package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/internal/uerr"
)

// trackProgress starts a ticker that reports an estimated completion
// fraction for step name to the console, based on estimate. estimate <= 0
// means no history exists for this step; trackProgress then does nothing,
// and the console keeps showing the plain state pill with no bar. The
// returned stop func must be called exactly once, when the step's plugin
// call returns; it also stops the ticker if ctx is canceled first.
func (r *run) trackProgress(ctx context.Context, name string, estimate time.Duration) func() {
	if estimate <= 0 {
		return func() {}
	}

	ctx, cancel := context.WithCancel(ctx)
	start := time.Now()
	go func() {
		ticker := time.NewTicker(progressTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fraction := float64(time.Since(start)) / float64(estimate)
				if fraction > progressCap {
					fraction = progressCap
				}
				r.store.SetStepProgress(name, fraction)
			}
		}
	}()
	return cancel
}

// inputRows builds one Detail row per top-level field of raw (name's
// declared `with:` block, before rendering), pairing it with rendered's
// corresponding value. A field is Sensitive when schemaSensitive names it,
// or when any of its "${needs.../setup.../vars...}" markers resolve to a
// sensitive value in scopes or name a variable sensitiveVars marks. A
// `with` block with no fields (a step that declared none) reports no rows.
func inputRows(step config.Step, rendered json.RawMessage, scopes expr.Scopes, schemaSensitive, sensitiveVars map[string]bool) ([]session.Detail, error) {
	if len(step.With) == 0 {
		return nil, nil
	}
	tpl, err := step.WithTemplate()
	if err != nil {
		return nil, fmt.Errorf("with: %w", err)
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(step.With, &rawFields); err != nil {
		return nil, fmt.Errorf("with: decode: %w", err)
	}
	var renderedFields map[string]json.RawMessage
	if err := json.Unmarshal(rendered, &renderedFields); err != nil {
		return nil, fmt.Errorf("with: decode rendered: %w", err)
	}

	names := make([]string, 0, len(rawFields))
	for field := range rawFields {
		names = append(names, field)
	}
	sort.Strings(names)

	rows := make([]session.Detail, 0, len(names))
	for _, field := range names {
		sensitive := schemaSensitive[field] || tpl.FieldSensitive(field, scopes, sensitiveVars)
		rows = append(rows, session.Detail{
			Label:     field,
			Value:     scalarString(renderedFields[field]),
			Sensitive: sensitive,
			Copyable:  !sensitive,
		})
	}
	return rows, nil
}

// scalarString renders one with-field's rendered value for display: a JSON
// string unquotes to its bare text, anything else (a number, bool, object,
// array) keeps its compact JSON form.
func scalarString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// outputRows converts a step's outputs into the rows its detail dialog
// shows, sorted by key for a stable order across renders.
func outputRows(values map[string]output.Value) []session.Detail {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]session.Detail, 0, len(keys))
	for _, k := range keys {
		v := values[k]
		rows = append(rows, session.Detail{Label: k, Value: v.String, Sensitive: v.Sensitive, Copyable: !v.Sensitive})
	}
	return rows
}

// reportUpFailure marks name Failed and logs err, unless ctx is already
// canceled - then err is just this step's in-flight call reacting to the
// same shutdown that's tearing down the whole run, not a genuine failure.
// markSkipped gives an unreported step this treatment its final state.
func (r *run) reportUpFailure(ctx context.Context, name string, err error) {
	if ctx.Err() != nil {
		return
	}
	msg := uerr.Display(err)
	r.emit(name, "failed: "+msg)
	r.store.SetStep(name, session.Failed, msg)
}

// failUp reports that name's Up returned err. A step that failed on its own
// is settled and left to its plugin; a step that Up canceled stays started,
// and shutdown calls Down for it.
func (r *run) failUp(ctx context.Context, name string, err error) {
	// Read the context before the report: the report reaches a watcher,
	// which can cancel the run.
	canceled := ctx.Err() != nil
	r.reportUpFailure(ctx, name, err)
	if !canceled {
		r.state.MarkSettled(name)
	}
}

// warnDenied names the hosts that the proxy denied since start, after the Up
// of step name failed, unless a canceled run caused the failure. Another step
// can have made some of the requests.
func (r *run) warnDenied(ctx context.Context, name string, start time.Time) {
	if ctx.Err() != nil {
		return
	}
	var hosts []string
	for _, req := range session.DeniedSince(r.store.Snapshot().Requests, start) {
		if !slices.Contains(hosts, req.Host) {
			hosts = append(hosts, req.Host)
		}
	}
	if len(hosts) == 0 {
		return
	}
	r.emit(name, fmt.Sprintf("warning: the proxy denied requests to %s while %s was starting; to allow them, add the hosts to proxy.egress.allow in kevin.cue",
		strings.Join(hosts, ", "), name))
}

// onEvent returns a handler that renders the log and progress events of one
// plugin. A step's own log lines go to the web console and the durable log
// file, not to the terminal - the terminal only narrates kevin's own DAG
// progress (up/ready/failed/removed, elsewhere in this package).
func (r *run) onEvent(name string) stepEvents {
	return stepEvents{r: r, name: name}
}

// stepEvents is the step.Events of one step's Up or Down call.
type stepEvents struct {
	r    *run
	name string
}

var _ step.Events = stepEvents{}

func (e stepEvents) Log(stream, text string, _ time.Time) {
	e.r.store.Log(e.name, stream, text)
	e.r.stepLog.Info(text, "step", e.name, "stream", stream)
}

func (e stepEvents) Progress(label string, current, total int64) {
	if total > 0 {
		e.r.emit(e.name, fmt.Sprintf("%s (%d/%d)", label, current, total))
		return
	}
	e.r.emit(e.name, label)
}

func (r *run) emit(step, text string) {
	_, _ = fmt.Fprintf(r.events, "%-16s %s\n", step, text)
}
