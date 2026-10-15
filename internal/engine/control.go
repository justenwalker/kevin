package engine

import (
	"context"
	"errors"
	"time"

	"github.com/justenwalker/kevin/internal/runapi"
	"github.com/justenwalker/kevin/internal/session"
)

// Rerun runs a step again, as RerunStep does, and reports what changed.
//
// A rerun that ran and left a step failed, the targeted step or a cascaded
// dependent, is a result: the failure is already in the session, and Failed
// is set when the targeted step is the one that failed. Rerun returns an
// error when the rerun did not start (session.ErrStepBusy, which a cascaded
// dependent that is busy also reports, and session.ErrUnknownStep), or when
// it stopped for another reason, such as a canceled context.
func (r *run) Rerun(ctx context.Context, req runapi.RerunRequest) (runapi.RerunResult, error) {
	before := stepStates(r.store.Snapshot().Steps)
	start := time.Now()

	err := r.RerunStep(ctx, req.Step, req.Cascade)
	if errors.Is(err, session.ErrStepBusy) || errors.Is(err, session.ErrUnknownStep) {
		return runapi.RerunResult{}, err
	}

	if err != nil && ctx.Err() != nil {
		return runapi.RerunResult{}, err
	}

	v := r.store.Snapshot()
	result, anyFailed := rerunResult(req.Step, before, v.Steps)
	if err != nil && !anyFailed {
		return runapi.RerunResult{}, err
	}
	for _, d := range session.DeniedSince(v.Requests, start) {
		result.Denied = append(result.Denied, runapi.DeniedRequest{Time: d.Time, Method: d.Method, Host: d.Host, Path: d.Path})
	}
	return result, nil
}

// rerunResult diffs steps against their states before the rerun. It also
// reports whether a step the rerun touched ended failed.
func rerunResult(target string, before map[string]string, steps []session.Step) (runapi.RerunResult, bool) {
	result := runapi.RerunResult{Step: target}
	var anyFailed bool
	for _, st := range steps {
		touched := st.Name == target || before[st.Name] != stepState(st)
		if touched && st.State == session.Failed {
			anyFailed = true
		}
		if st.Name == target && st.State == session.Failed {
			result.Failed = true
		}
		if touched {
			result.Changed = append(result.Changed, runapi.StepSummaryOf(st))
		}
	}
	return result, anyFailed
}

// stepStates maps each step to its state and message.
func stepStates(steps []session.Step) map[string]string {
	out := make(map[string]string, len(steps))
	for _, st := range steps {
		out[st.Name] = stepState(st)
	}
	return out
}

func stepState(st session.Step) string {
	return string(st.State) + "\x00" + st.Message
}
