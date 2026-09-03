# ADR-0003: Structured values over pre-formatted strings in the wire protocol

**Status:** Accepted

## Context

`ExportResult`, the plugin-protocol message a step uses to report how to
reach what it created, originally carried two parallel shapes of the same
data: `Env map[string]string`, pre-formatted `KEY=VALUE`-style environment
variables for `kevin connect` to inject into a shell, and `Out map[string]
Value`, the same data in structured form for another step's cross-scope
`needs` to consume. Every plugin author implementing `Export` had to decide
what belonged in each map, and `kevin connect`'s replacement, `kevin do`,
turned out to need only `Out` - it renders `${needs...}` expressions
against structured values and execs the result, no shell-formatted string
in the middle at all.

## Decision

A wire message reports data in one structured shape, not a shape plus a
pre-formatted, consumer-specific string rendering of the same shape. When a
value carries a property like secrecy that varies per-value, that property
is a field on the structured value, not something encoded into the string
itself (a naming convention, a redaction placeholder baked into the text) or
tracked in a side channel.

## Why

A pre-formatted string commits to one consumer's needs (shell-quoting rules,
a specific env-var naming convention) at the producer end, where the
producer has the least context about how the value will actually be used.
A structured value lets every consumer format it however it needs to,
and lets a cross-cutting property like "never log this" travel with the
value instead of being re-derived from a naming convention at each call
site.

**DO** (`protos/pb/plugin.proto`):
```protobuf
message Value {
  oneof kind {
    string string_value = 1;
  }

  // Sensitive marks this value as secret: it must never be logged or
  // displayed in full. It sits outside "kind" - sensitivity is a property
  // of any value, not a kind of value.
  bool sensitive = 10;
}
```
`plugin.Value` (`plugin/value.go`) mirrors this on the Go side: a `String`
or a `Sensitive`-wrapped `Value`, both satisfying the same interface, so a
consumer asks `v.IsSensitive()` and gets `[REDACTED]` back from `String()`
automatically instead of every caller having to know which env-var names
are secret by convention.

**DO NOT** (`plugin/plugin.go`, before `feat(plugin): drop ExportResult.Env,
Export now reports only structured Out`, c0156f4):
```go
// ExportResult carries what a step exports: Env, the environment variables
// an external command needs to reach it (what "kevin connect" uses), and
// Out, the same outputs in structured form for another step's cross-scope
// needs to consume - the same Value shape Outputs uses.
type ExportResult struct {
	Env map[string]string
	Out map[string]Value
}
```
Every plugin author had to populate both maps, `KUBECONFIG=/path`-style
strings with no way to mark one entry sensitive independent of its
neighbors, alongside the structured `Out` that already carried the same
data with that property intact.

## Consequences

A consumer that genuinely wants a shell-ready string (an env var to
`exec.Cmd.Env`, say) formats it at the point of use, from the structured
value, rather than the protocol carrying a pre-baked version for it. That
puts a small amount of formatting work on each consumer instead of on the
producer, in exchange for one shape of data flowing through the wire
protocol instead of two that can drift out of sync.
