---
name: writing-a-plugin
description: How to write a kevin plugin - Provider/Step API, plugin.Serve, schema.cue. Use when creating a new kevin plugin binary.
---

A plugin is a standalone binary. It builds a `plugin.Plugin` and calls
`plugin.Serve`, which speaks the kevin plugin protocol over gRPC on stdio
(see `docs/site/content/docs/extending/plugin-protocol.md` for the wire
contract - `Info`, `Configure`, `Up`, `Down`, `Export`). A provider offers
one or more step types; one process serves every step type its provider
offers, so a `Step` must be safe for concurrent use - the DAG can create
several steps of the same type at once.

`cmd/kevin-plugin-echo` is a complete, tested reference: three step types
(`echo`, `fail`, `probe`), a provider `config` block, `schema.cue` +
`config_schema.cue`, an icon, and full unit tests. Copy its shape rather
than starting from nothing.

## Minimal skeleton

```go
package main

import (
	"context"

	"github.com/justenwalker/kevin/plugin"
)

type widgetStep struct{}

var _ plugin.Step = widgetStep{}

func (widgetStep) Schema() []byte { return schema } // //go:embed schema.cue

func (widgetStep) Kind() plugin.StepKind { return plugin.StepKindResource }

func (widgetStep) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config) // json.Unmarshal into your own config struct
	if err != nil {
		return nil, err
	}
	out.Log("stdout", "creating "+req.Step)
	return &plugin.Result{Outputs: plugin.StringMap(map[string]string{"endpoint": "..."})}, nil
}

func main() {
	plugin.Serve(plugin.Plugin{
		Name:    "acme", // must match the plugins: key in kevin.cue
		Version: "1.0.0",
		Steps:   map[string]plugin.Step{"widget": widgetStep{}},
	})
}
```

A step names this type as `uses: "acme:widget"`; a `plugins:` entry sets
`cmd` (or `file`/`oci`/`http` for a distributed package - see AGENTS.md's
"Environment files" section, not this skill's concern).

## The `Step` interface, and the three optional ones

```go
type Step interface {
	Schema() []byte                                              // nil if no with-block config
	Kind() StepKind
	Up(ctx context.Context, req *UpRequest, out Emitter) (*Result, error)
}
```

`Info` reports which optional interfaces a step type implements, so the
engine never has to call one to find out and see what happens:

- **`Downer`** — `Down(ctx, *DownRequest, Emitter) error`. Implement this
  when `Up` creates something that needs removing. Must be idempotent: the
  engine may call it for a step that never came up, or one already gone.
  A pure action/probe type (nothing to tear down) simply doesn't implement
  it - the engine's teardown walk skips the call entirely rather than
  calling a no-op. See `errors.go`'s `failStep` (implements only `Step`)
  vs. `echo.go`'s `echo` (also implements `Downer`).
- **`Exporter`** — `Export(ctx, *ExportRequest) (*ExportResult, error)`.
  Implement this when something `Up` created is reachable from outside
  kevin (e.g. a kubeconfig). Backs `kevin connect`. Must not create or
  change anything - only report how to reach what `Up` already made.
- **`IdempotentStep`** — `Idempotent() bool`. Implement this when calling
  `Up` again is always safe (a probe that just re-checks, e.g.). Lets the
  console sweep this step into a cascading rerun of something it depends
  on, not just a direct rerun. See `probe.go`'s `probeStep`.

## `StepKind` - classification only, no behavior change

| Value | Meaning |
|---|---|
| `StepKindResource` | `Up` creates and destroys something. Most step types. |
| `StepKindAction` | `Up` mutates state it doesn't own, no lifecycle of its own (e.g. applying a manifest to someone else's cluster). |
| `StepKindProbe` | `Up` creates nothing, only checks that something else is ready. |

## Request/response cheat sheet

- **`Env`** (embedded in every request, identical for every step in a
  session): `Project`, `Workspace` (`.kevin` dir), `Network` (shared
  docker network), `CAPath`, `HTTPProxyAddr`, `ConsoleAddr`, `ProxyEnv`,
  `Domain`, `Relay`, `ProjectDir` (resolve a relative `with`-block path
  against this).
- **`UpRequest.Deps`** — `map[string]map[string]Value`, keyed by every
  step named in this step's `needs:` list, holding that step's published
  `Outputs`. Same data a `${needs.<step>.out.<key>}` expression reads.
- **`DownRequest.Outputs`** — only this step's own prior outputs, replayed
  from state, so `Down` works even after an engine restart.
- **`Result`** — `Outputs map[string]Value` (read by dependents' `Deps`
  and `${...}`), `Routes []Route` (joins the proxy's routing table),
  `ExposedPorts []ExposedPort` (raw TCP/UDP published to the host),
  `EgressAllow []string` (hosts this step itself may reach - proxy denies
  by default), `Details []Detail` (the *only* channel that reaches the
  console card; `Route`/`ExposedPort` are purely functional and don't
  auto-populate it - call their own `.Detail()` method for a sensible
  starting row, or build one by hand).

## Values and secrets

Every `Value` in `Outputs`, `Deps`, or a `Detail` is `plugin.String(...)`
or `plugin.Sensitive{...}` wrapping one:

```go
result.Outputs = plugin.StringMap(map[string]string{"port": "8080"}) // plain
result.Outputs["token"] = plugin.Sensitive{plugin.String(tok)}       // secret
```

A `Sensitive` value's `String()` returns `"[REDACTED]"`, so an accidental
`fmt`/`log` of it - even nested in a map, slice, or struct - can't leak
it; only `.Reveal()` does. The engine carries the marking through to
dependents' `Deps` and never shows it on the console card or a log in
full. This covers structured data the engine handles - a plugin that
prints its own secret to stdout/stderr defeats it like any other tool
would.

## Schema and provider config

`Step.Schema()` returns CUE constraining that step type's `with` block -
`nil` if it takes none. Convention: a `schema.cue` file declaring
`#Config: { field?: type }`, embedded with `//go:embed`. Optional fields
use `?:`; the engine unifies the caller's `with` block against this
before `Up` ever runs, so a malformed environment fails before any
plugin call.

A provider with its own config (delivered once via `Configure`, before
any step of that provider runs) sets `Plugin.ConfigSchema` +
`Plugin.Configure`:

```go
plugin.Plugin{
	ConfigSchema: configSchema, // //go:embed config_schema.cue
	Configure: func(ctx context.Context, config []byte, env plugin.Env) error {
		var cfg providerConfig
		return json.Unmarshal(config, &cfg) // store it somewhere Up can read
	},
}
```

`Plugin.Icon` is an optional small PNG (48×48 or less) shown next to the
provider's step types on the console; see `config.go`'s `demoIcon()` for
a generated-not-checked-in example. No icon shows a puzzle-piece
placeholder instead.

## Error handling

House style ([`docs/GO_CONVENTIONS.md`](../../../docs/GO_CONVENTIONS.md),
`GO-###` rules) applies to plugin code too - sentinel errors as
`type Error string`, not `errors.New` vars:

```go
type Error string
func (e Error) Error() string { return string(e) }
const ErrRequested = Error("acme: failure requested by configuration")
```

## Testing

No harness needed - call the `Step` method directly with a fake
`Emitter`. See `echo_test.go` for the pattern: a small `capture` type
implementing `Emitter` to collect `Log`/`Progress` calls, table-driven
`t.Run` subtests, `plugin.StringMap(...)` for expected `Outputs`. Assert
`Schema()` contains `"#Config"` as a cheap sanity check that it's valid
CUE declaring the right root.

## Wiring one binary with several step types

```go
plugin.Serve(plugin.Plugin{
	Name:    "echo",
	Version: version,
	Steps: map[string]plugin.Step{
		"echo":  echo{},
		"fail":  failStep{},
		"probe": probeStep{},
	},
	Icon:      icon,
	Configure: configure,
})
```

One `Plugin.Name`, many `Steps` keys - each becomes `uses:
"<name>:<step-key>"`. Not every step type needs the same optional
interfaces; mix and match per type, as `echo`/`failStep`/`probeStep` do.

## Reference material, in order of usefulness

1. `cmd/kevin-plugin-echo/` - every pattern above, working and tested.
2. `plugin/plugin.go` - the full SDK type definitions (`Env`, `UpRequest`,
   `Result`, `Step`, `Downer`, `Exporter`, `IdempotentStep`, ...).
3. `plugin/value.go` - `Value`, `String`, `Sensitive`, `StringMap`.
4. `docs/site/content/docs/extending/writing-a-plugin.md` and
   `plugin-protocol.md` - the prose version of this skill, plus the wire
   protocol and session-startup sequence.
5. A real builtin for a heavier example: `internal/plugins/container` (a
   resource with `Down`), `internal/plugins/kind` (`Export` + relay),
   `internal/plugins/wait` (a probe).
