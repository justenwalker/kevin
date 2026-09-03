# ADR-0001: Breaking changes over compatibility shims, pre-1.0

**Status:** Accepted

## Context

kevin is pre-1.0. Every rename, removal, or reshaping of a field, RPC, or
CUE schema key has come up against the same fork in the road: keep the old
name around as a deprecated alias so nothing breaks, or delete it and fix
every call site in the same commit. A deprecated alias costs nothing today
and something forever - every reader of the code has to learn there are two
names for the same thing, and the "why does this still exist" question has
no good answer once the alias has outlived whoever added it.

## Decision

Do the full sweep instead: proto, Go code, mocks, tests, docs, and examples,
all in the commit that makes the change. No reserved proto fields, no
deprecated aliases, no backward-compatibility layer, unless a human
explicitly asks for one. This is a deliberate pre-1.0 policy, not a
permanent one - it stops applying once kevin ships a 1.0 with users who
can't be asked to just update.

## Why

A compatibility shim is a bet that keeping an old caller working is worth
more than the clarity of having exactly one way to do something. Pre-1.0,
that bet is almost always wrong: there is no installed base to protect, and
every shim is a second, subtly different path through code that a future
change has to keep in sync with the first.

**DO** (`internal/config/config.go`, `relay: always run it, drop the enabled
toggle`, f82d5ca):
```go
// Relay configures the in-network relay.
type Relay struct {
	Image string `json:"image"`
}
```
The field is gone. `relay.enabled` is gone from `kevin.cue` too, along with
every nil-guard and disabled-check that existed only to serve it
(`relayAddr`/`closeRelay`'s wrappers, `closeSetupRelay`'s disabled check).

**DO NOT:**
```go
// Relay configures the in-network relay.
type Relay struct {
	Image string `json:"image"`

	// Deprecated: the relay always runs now. This field is ignored and
	// will be removed in a future version.
	Enabled bool `json:"enabled,omitempty"`
}
```
This keeps an old `kevin.cue` from erroring, at the cost of a field that
lies about doing anything, a comment explaining why it lies, and a
"future version" that never arrives because removing it always looks
riskier than leaving it.

A structural example of the same call: `builtin:trust` was a DAG step type
with an `Up`/`Down` lifecycle for installing the CA into the system trust
store (ee30844). CA trust is host-machine state, not a per-project
resource - the step type was deleted outright and replaced with `kevin ca
install`/`uninstall` subcommands, not kept as a deprecated alias alongside
the new commands.

## Consequences

Every environment file and every plugin written against an older kevin
needs updating when a breaking change lands - there is no soft landing.
That cost is accepted deliberately in exchange for a codebase with no dead
paths kept alive only for compatibility. `AGENTS.md`'s "Project Status:
pre-1.0" section is the durable instruction this ADR expands on.
