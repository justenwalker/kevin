# ADR-0002: Type safety over runtime validation

**Status:** Accepted

## Context

kevin's DAG engine, CEL expression renderer, and CUE schema layer all have
the same shape of choice available whenever a value's constraints are
knowable ahead of time: check it once, statically, before anything runs -
or let it flow through as an untyped/stringly-typed value and discover the
problem at runtime, potentially mid-`Up`, with a container or cluster
already created.

`${setup.cluster.out.x}` referencing a `setup.cluster` that a step's `needs`
list never declared used to pass `kevin validate` clean and fail only when
that step actually ran, mid-DAG, with `no such key: cluster` - after other
steps may have already stood up real Docker resources. Both facts a
correctness check would need - which steps a `with` block's `${...}`
markers reference, and which steps `needs` actually declares - are fully
static in the file. Nothing about them needs a plugin process or a live
Docker resource to check.

## Decision

When a value's shape or its cross-references are knowable from the file
alone, check them statically - at config-validate time, or in the type
system itself - instead of deferring to a runtime check that only fires
once something is already running. Prefer a named, typed field over a
position in an untyped list or map when the compiler can enforce it.

## Why

A runtime-only check turns a typo into a mid-run failure with partially
created resources instead of an immediate, cheap `kevin validate` error.
A typed field lets the compiler catch a caller passing the wrong thing,
where an untyped map or a positional argument list only surfaces the
mistake by way of a wrong value silently flowing through.

**DO** (`internal/expr/expr.go`, `feat(config): validate needs.<step>/
setup.<name> references statically`, c96b20c):
```go
// ReferencedSteps reports every step name that raw's "${...}" markers
// reference via "needs.<step>..." or "setup.<step>...", with no
// evaluation - for a caller that wants to check those names against a
// step's own needs list statically, before any of Render's variables
// (upstream outputs, in particular) exist to evaluate against.
func ReferencedSteps(raw json.RawMessage) ([]string, []string, error) { ... }
```
`internal/config`'s `validateStep` calls this for every step and checks
each referenced name against that step's own `needs`, before schema
compilation - so a bad reference fails `kevin validate`, not `kevin run`.

**DO NOT:**
```go
// Render a step's with block and let evaluation fail naturally if a
// reference doesn't resolve. Simpler to write, but the error only
// surfaces once this step's Up actually runs - by which point every
// upstream step in the DAG may already exist as real Docker resources
// that now need tearing down because of a typo in a string.
with, err := renderWith(step, deps, setupDeps)
```

A second example, in the type system rather than a validator: an `env`
step's `needs` can name a `setup.<name>` entry, but `needs`'s own type is a
uniform three-level map that can't hold the extra per-name level a
cross-scope reference needs without losing static typing (9ab14ae). Rather
than stringly-typing `needs` to fit the extra case, the engine added a
sibling `setup` CEL variable at the same level as `needs`/`env`/`project` -
`${setup.<name>.out.<key>}`, not `${needs.setup.<name>...}` - keeping both
representations fully typed instead of collapsing one into a map that has
to be re-parsed at read time.

## Consequences

A static check has to be kept honest against the thing it's checking - the
commit that added `ReferencedSteps` reused `renderString`'s own
marker-splitting logic (extracted into `nextMarker`) specifically so the
validator and the renderer can't drift apart on what counts as a marker. A
static check that quietly diverges from runtime behavior is worse than no
check at all, since it teaches users to distrust `kevin validate`.
