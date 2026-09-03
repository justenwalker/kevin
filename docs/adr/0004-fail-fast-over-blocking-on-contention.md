# ADR-0004: Fail fast over blocking on contention

**Status:** Accepted

## Context

Two places in the engine hit the same question: what to do when a caller
wants something that's currently in use, or a resource that was explicitly
pinned to a specific value isn't available. A console-triggered rerun could
land on a step whose `Up` is already in flight. A `proxy.gateway_port`
pinned in `kevin.cue` could already be bound by something else on the host.
In both cases the alternative to failing immediately is blocking: wait for
the lock, or retry the bind until it succeeds or times out.

## Decision

When a caller collides with in-flight work on the same resource, reject
immediately instead of queuing behind it, unless the caller explicitly
needs to wait. When a resource was explicitly pinned by the user (a port
number, a name), a failure to acquire it is a hard error, not something to
retry or silently work around - GO-019 already states the general Go-level
form of this: prefer `TryLock` over a blocking lock unless blocking is
explicitly required.

## Why

A step's `Up` can take minutes (pulling an image, waiting for a cluster).
Blocking a second caller behind that lock leaves it hanging with no
feedback for as long as the first call takes, indistinguishable from a
hang. Failing immediately gives the caller an actionable error right away -
the step is busy, try again once it's done - instead of a silent wait of
unknown length. A pinned resource is different: the user said "this exact
port," so a fallback to some other port would silently give them something
other than what they asked for. The correct move there is to say so and
stop, not to route around it.

**DO** (`internal/engine/engine.go:1172`, step rerun contention):
```go
func (r *run) upStep(ctx context.Context, name string, deps map[string]dag.Outputs) (dag.Outputs, error) {
	mu := r.stepLock(name)
	if !mu.TryLock() {
		return nil, fmt.Errorf("%s: %w", name, session.ErrStepBusy)
	}
	defer mu.Unlock()
	...
}
```

**DO** (`internal/engine/engine.go:517`, pinned port):
```go
pinned := opts.GatewayPort != 0
port := opts.GatewayPort
if !pinned {
	port = loadGatewayPort(opts.Workspace)
}
gatewayLn, err := lc.Listen(ctx, "tcp", net.JoinHostPort(gateway.String(), strconv.Itoa(port)))
if err != nil && !pinned {
	// The recorded port may no longer be free - a stale reservation, or
	// another project's process. Let the OS pick one instead.
	gatewayLn, err = lc.Listen(ctx, "tcp", net.JoinHostPort(gateway.String(), "0"))
}
```
An unpinned port falls back to the OS picking a fresh one. A pinned port
gets no such fallback - `err` from the pinned bind propagates straight out
as a hard error, because the user asked for that port on purpose.

**DO NOT:**
```go
func (r *run) upStep(ctx context.Context, name string, deps map[string]dag.Outputs) (dag.Outputs, error) {
	mu := r.stepLock(name)
	mu.Lock() // blocks until whatever's already running finishes
	defer mu.Unlock()
	...
}
```
A console click that races an in-flight `Up` now just hangs, with nothing
to tell the caller it's queued rather than stuck.

## Consequences

A caller that collides with in-flight work has to handle `ErrStepBusy` and
retry on its own terms (the console does, by surfacing it and letting the
user click again), rather than the engine handling the wait for it. That
trade is deliberate: an explicit "busy, try later" is more useful to a
caller than an unbounded wait, and a genuinely-needed wait is opted into at
the call site, not the default.
