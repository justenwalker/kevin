# ADR-0006: A single struct plus error, not multi-value returns

**Status:** Accepted

## Context

Several call shapes in the engine and plugin SDK return more than one piece
of data alongside `error`: what a step published when it came up (outputs,
routes, exposed ports, egress allowlist), what a step exports for a
cross-scope consumer, what an MCP tool call returned. Each of these could
be a growing list of positional return values, or a single named struct.
`Render`'s helper in `internal/expr` hit this directly: its positional
argument list grew to four (`deps, system, setupDeps`, then `project`)
before being replaced with a named `expr.Scopes` struct.

## Decision

A function that would return three or more values (not counting `error`)
returns a single struct plus `error` instead. This is GO-019 in
`GO_CONVENTIONS.md`; this ADR is the project-wide version of the same call,
since it shows up above the level of a single function signature too, in
protocol message shapes like `UpResult`/`ExportResult`/`ToolCallResult`.

## Why

A third or fourth positional return value means every call site
destructures all of them in order, even a caller that wants only one -
and a reader checking what changed has to recount positions instead of
reading field names. A named struct is self-documenting at the call site
and in godoc, and adding a field later doesn't reshuffle every existing
caller's positional list.

**DO** (`plugin/plugin.go:277`):
```go
// Result is what a successful Up publishes.
type Result struct {
	// Outputs are the values that the dependents of this step can read.
	Outputs map[string]Value

	// Routes are the hostnames that this step serves.
	Routes []Route

	// ExposedPorts are raw TCP or UDP endpoints that this step publishes
	// directly to the host.
	ExposedPorts []ExposedPort

	// EgressAllow lists the external hosts that this step can reach.
	EgressAllow []string
	...
}

func (s Step) Up(ctx context.Context, req UpRequest) (Result, error) { ... }
```

**DO NOT:**
```go
func (s Step) Up(ctx context.Context, req UpRequest) (
	outputs map[string]Value,
	routes []Route,
	exposedPorts []ExposedPort,
	egressAllow []string,
	err error,
) { ... }
```
A caller that wants only `Outputs` still has to name all four other
results, and a fifth field means editing every call site's positional
list, not just the struct literal that builds it.

A second example, at a function-argument level rather than a return value:
`internal/expr`'s `Render` had a positional argument list that grew to four
(`deps`, `system`, `setupDeps`, then `project`, in commit 049afe1) before
being replaced with `expr.Scopes`, a named struct, so the caller-supplied
values are named rather than positional on the way in too - the same
problem, and the same fix, on the input side of a call instead of the
output side.

## Consequences

A struct return means a caller that wants to ignore most of the result
still gets the whole thing and picks the field it needs, rather than
`_`-ing out positional values it doesn't want - a minor verbosity trade in
exchange for names surviving a refactor instead of positions.
