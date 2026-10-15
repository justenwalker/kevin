package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/dag"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/proxy"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/step"
	"github.com/justenwalker/kevin/internal/uerr"
)

// upPlan is everything a step's Up call needs, resolved before it runs.
type upPlan struct {
	name    string
	ref     config.StepRef
	client  stepPlugin
	req     step.UpRequest
	timeout time.Duration
}

// planUp resolves name's cross-scope needs and renders its with block into the
// request for Up, and publishes the rendered inputs. A failure is reported to
// the session.
func (r *run) planUp(ctx context.Context, name string, deps map[string]output.Map) (upPlan, error) {
	cfgStep := r.steps[name]
	ref := cfgStep.Uses

	cross, err := r.crossScopeDeps(ctx, name)
	if err != nil {
		r.reportUpFailure(ctx, name, err)
		return upPlan{}, err
	}
	setupDeps, setupContainers := cross.Outputs, cross.Containers

	with, err := r.renderWith(name, cfgStep, deps, setupDeps)
	if err != nil {
		r.reportUpFailure(ctx, name, err)
		return upPlan{}, err
	}
	schemaSensitive, schemaErr := sensitiveWithFields(stepSchema(r.caps[ref.Plugin], ref.Step))
	if schemaErr != nil {
		r.emit(name, "warning: inputs: "+uerr.Display(schemaErr))
	} else if rows, rowsErr := inputRows(cfgStep, with, r.scopesFor(name, deps, setupDeps), schemaSensitive, r.cfg.SensitiveVariables()); rowsErr != nil {
		r.emit(name, "warning: inputs: "+uerr.Display(rowsErr))
	} else {
		r.store.SetStepInputs(name, rows)
	}

	return upPlan{
		name:   name,
		ref:    ref,
		client: r.plugins[ref.Plugin],
		req: step.UpRequest{
			Step:       name,
			Type:       ref.Step,
			Env:        r.env,
			Config:     with,
			Deps:       depsWithSetup(deps, setupDeps),
			Containers: r.stepContainersFor(deps, setupContainers),
		},
		timeout: time.Duration(cfgStep.Timeout),
	}, nil
}

// callUp calls Up for plan, bounded by its timeout when positive, and reports
// a failure. A step whose Up outlived its timeout is not settled, so shutdown
// calls Down for it.
func (r *run) callUp(ctx context.Context, plan upPlan, start time.Time) (step.Result, error) {
	upCtx := ctx
	if plan.timeout > 0 {
		var cancel context.CancelFunc
		upCtx, cancel = context.WithTimeout(ctx, plan.timeout)
		defer cancel()
	}
	result, err := plan.client.Up(upCtx, plan.req, r.onEvent(plan.name))
	if err == nil {
		return result, nil
	}
	// The parent ctx, not upCtx, tells a timeout from an interrupted run.
	if ctx.Err() == nil && errors.Is(upCtx.Err(), context.DeadlineExceeded) {
		err = uerr.Wrap(fmt.Errorf("%s: %w", plan.name, session.ErrStepTimeout),
			"timed out after %s; raise timeout in kevin.cue", plan.timeout)
		r.reportUpFailure(ctx, plan.name, err)
		return step.Result{}, err
	}
	r.failUp(ctx, plan.name, err)
	r.warnDenied(ctx, plan.name, start)
	return step.Result{}, err
}

// upStep calls Up on one step. It is the dag.NodeFunc both the initial
// bring-up and a console-triggered rerun run through, so a rerun gets route
// registration, egress allow, detail/progress reporting, and timing history
// exactly like the first attempt. On success it also records its own
// output into r.state directly, rather than leaving that to the
// walk's aggregate return value.
func (r *run) upStep(ctx context.Context, name string, deps map[string]output.Map) (_ output.Map, err error) {
	if grp, ok := r.groups[name]; ok {
		var out output.Map
		out, err = r.upGroup(ctx, name, grp, deps)
		if err == nil {
			// Record as soon as this step succeeds: up's Walk and
			// RerunStep's WalkFrom run every step in one goroutine group
			// with no ordering guarantee between them, so a slow step
			// still in flight elsewhere would otherwise leave this one's
			// output unrecorded for as long as that other step runs - a
			// rerun requested on anything else in that window would see
			// no prior output for a dependency that is, in fact, long
			// since Ready.
			r.state.Complete(map[string]output.Map{name: out})
		}
		return out, err
	}

	unlock, ok := r.state.TryLock(name)
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, session.ErrStepBusy)
	}
	defer unlock()

	// A step that fails to come up owns no routes: a rerun's old ones stay
	// only until it fails.
	defer func() {
		if err != nil {
			r.clearRoutes(ctx, name)
		}
	}()

	plan, err := r.planUp(ctx, name, deps)
	if err != nil {
		return nil, err
	}

	estimate, _ := r.timings.EstimateUp(name, plan.ref.String())
	start := time.Now()
	stop := r.trackProgress(ctx, name, estimate)
	defer stop()

	r.emit(name, "up")
	r.store.SetStep(name, session.Running, "")
	// A rerun's Up republishes whatever detail rows are still true; clear
	// the old ones first so a rerun doesn't pile a second copy onto them.
	r.store.ClearStepDetails(name)

	r.state.MarkStarted(name)
	result, err := r.callUp(ctx, plan, start)
	if err != nil {
		return nil, err
	}
	return r.publish(ctx, plan, result, start)
}

// publish applies result to the relay, proxy, port forwards and session, marks
// the step Ready, and records its outputs.
func (r *run) publish(ctx context.Context, plan upPlan, result step.Result, start time.Time) (output.Map, error) {
	name := plan.name
	if relayErr := r.wireRelay(ctx, name, result); relayErr != nil {
		return nil, relayErr
	}
	r.state.SetContainers(name, result.Containers)
	systemThis := output.Map{}
	r.closeStepForwards(name)
	if err := r.exposePorts(ctx, name, result.ExposedPorts, systemThis); err != nil {
		return nil, err
	}
	if len(systemThis) > 0 {
		r.state.SetSystem(name, systemThis)
	}

	for _, d := range result.Details {
		r.emit(name, "detail: "+d.Label)
		r.store.AddStepDetail(name, sessionDetail(d))
	}

	r.emit(name, "ready")
	r.store.SetStep(name, session.Ready, "")
	r.timings.RecordUp(ctx, name, plan.ref.String(), time.Since(start))

	out := result.Outputs
	r.state.Complete(map[string]output.Map{name: out})
	r.store.SetStepOutputs(name, outputRows(out))
	return out, nil
}

// wireRelay registers result's routes with the host proxy, and tells the
// relay to transparently capture each of result's Containers' egress - a
// builtin:container step reports one, a builtin:kubernetes step one per node,
// treated identically here. A step with none (exec, a resourceless step)
// has nothing to register.
func (r *run) wireRelay(ctx context.Context, name string, result step.Result) error {
	if err := r.addRoutes(ctx, name, result); err != nil {
		return err
	}
	containers := result.Containers
	for _, c := range containers {
		netnsPath := c.NetnsPath
		if netnsPath == "" {
			continue
		}
		id := name
		if len(containers) > 1 {
			id = name + "/" + c.Name
		}
		if err := r.relay.RegisterCapture(ctx, id, netnsPath, c.ExcludeCIDRs); err != nil {
			r.reportUpFailure(ctx, name, err)
			return err
		}
	}

	ids := make([]string, 0, len(result.Faults))
	for _, f := range result.Faults {
		if err := r.relay.ApplyFault(ctx, f); err != nil {
			r.reportUpFailure(ctx, name, err)
			return err
		}
		ids = append(ids, f.ID)
	}
	r.state.SetFaults(name, ids)
	return nil
}

// sessionDetail is the console row for d.
func sessionDetail(d step.Detail) session.Detail {
	return session.Detail{
		Label:     d.Label,
		Value:     d.Value.String,
		Copyable:  d.Copyable,
		Href:      d.Href,
		Sensitive: d.Value.Sensitive,
	}
}

// proxyRoute is the proxy's form of rt.
func proxyRoute(rt step.Route) proxy.Route {
	mode := proxy.RouteModeMITM
	switch rt.Mode {
	case step.RouteModeMITM:
	case step.RouteModePassthrough:
		mode = proxy.RouteModePassthrough
	case step.RouteModeRaw:
		mode = proxy.RouteModeRaw
	}
	route := proxy.Route{Host: rt.Host, Upstream: rt.Upstream.Addr, TLS: rt.TLS, Mode: mode}
	if rt.Upstream.Relay != nil {
		route.Upstream = rt.Upstream.Relay.Target
		route.Relay = rt.Upstream.Relay.Relay
	}
	return route
}

// addRoutes replaces name's routes and egress hosts in the host proxy with
// result's, and, for a route marked Intercept, registers it with the relay
// too - its own DNS answers for the host, and it opens a listener for the
// route's declared ports.
func (r *run) addRoutes(ctx context.Context, name string, result step.Result) error {
	routes := make([]proxy.Route, 0, len(result.Routes))
	var intercepts []relay.Intercept
	for _, route := range result.Routes {
		routes = append(routes, proxyRoute(route))
		if route.Intercept != nil {
			intercepts = append(intercepts, relay.Intercept{Host: route.Host, Ports: route.Intercept.Ports})
		}
	}
	r.proxy.SetStep(name, routes, result.EgressAllow)
	for _, route := range result.Routes {
		r.emit(name, "serving https://"+route.Host)
	}

	if len(intercepts) == 0 && !r.state.Intercepting(name) {
		return nil
	}
	// A failed call may still have registered some, so the flag stays set
	// for clearRoutes to retry the release.
	if len(intercepts) > 0 {
		r.state.SetIntercepting(name, true)
	}
	if err := r.relay.SetIntercepts(ctx, name, intercepts); err != nil {
		r.reportUpFailure(ctx, name, err)
		return err
	}
	if len(intercepts) == 0 {
		r.state.SetIntercepting(name, false)
	}
	return nil
}

// clearRoutes removes the routes and egress hosts name owns, and releases
// its intercepts in the relay. Teardown runs no proxy and no relay, so either
// can be nil.
func (r *run) clearRoutes(ctx context.Context, name string) {
	if r.proxy != nil {
		r.proxy.ClearStep(name)
	}
	if r.relay == nil || !r.state.Intercepting(name) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), relayReleaseTimeout)
	defer cancel()
	if err := r.relay.SetIntercepts(ctx, name, nil); err != nil {
		r.emit(name, "warning: release intercepts: "+err.Error())
		return
	}
	r.state.SetIntercepting(name, false)
}

// relayReleaseTimeout bounds the relay call that releases a step's intercepts.
const relayReleaseTimeout = 10 * time.Second

func (r *run) up(ctx context.Context) error {
	results, err := dag.Walk(ctx, r.graph(), r.upStep, r.cfg.Engine.MaxParallel)
	r.markSkipped(r.graph().Steps(), results)
	return err
}

// clearFaults removes every fault name's last Up applied, regardless of
// whether name's own plugin implements Downer at all - a netem qdisc is
// relay-side state a plugin process has no way to reach directly (see
// wireRelay), so this must run engine-side, unconditionally, on the way
// down. Does nothing for a step that never applied one. A ClearFault
// failure (a namespace that's already gone, e.g. its container step was
// torn down first) is logged, not fatal - the fault is moot either way.
func (r *run) clearFaults(ctx context.Context, name string) {
	for _, id := range r.state.TakeFaults(name) {
		if err := r.relay.ClearFault(ctx, id); err != nil {
			r.emit(name, "warning: clear fault: "+err.Error())
		}
	}
}
