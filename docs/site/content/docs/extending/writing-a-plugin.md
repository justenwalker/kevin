---
title: "🧩 Writing a plugin"
weight: 1
---

# Writing a plugin

A plugin is a binary that builds a `plugin.Plugin` and hands it to `plugin.Serve`. A provider offers one or more step types, each a `plugin.Step`. See [`cmd/kevin-plugin-echo`](https://github.com/justenwalker/kevin/tree/main/cmd/kevin-plugin-echo) for a complete example, with three step types and a provider `config` block.

```go
package main

import (
    "context"

    "github.com/justenwalker/kevin/plugin"
)

type widgetStep struct{}

func (widgetStep) Schema() []byte { return []byte(`#Config: image!: string`) }

// Kind classifies what this step type is. See "Step kind" below.
func (widgetStep) Kind() plugin.StepKind { return plugin.StepKindResource }

func (widgetStep) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
    out.Log("stdout", "creating "+req.Step)
    return &plugin.Result{Outputs: map[string]plugin.Value{"endpoint": plugin.String("...")}}, nil
}

// Down is optional. See "Optional interfaces" below. widgetStep
// implements it because its Up creates something that needs removing.
func (widgetStep) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
    return nil
}

func main() {
    plugin.Serve(plugin.Plugin{
        Name:    "acme",
        Version: "1.0.0",
        Steps:   map[string]plugin.Step{"widget": widgetStep{}},
    })
}
```

## Step kind

`Kind` classifies what a step type is, for the console and for docs. It doesn't change engine behavior. Return one of three values: `plugin.StepKindResource` (`Up` creates and destroys something, most step types), `plugin.StepKindAction` (`Up` mutates state it doesn't own, with no lifecycle of its own; apply-only, like a manifest applied to someone else's cluster), or `plugin.StepKindProbe` (`Up` creates nothing, only checks that something else is ready).

## Optional interfaces

Two capabilities are optional, each its own interface a `Step` implements only when it applies. `Info` reports honestly which ones a step type has, instead of a caller finding out by calling and seeing what happens:

- **`plugin.Downer`**: a step type whose `Up` creates something that needs removing implements `Down(ctx, *plugin.DownRequest, Emitter) error`. `Down` must be idempotent: the engine can call it for a step that never came up, or one that's already gone. A step type with nothing to tear down, usually an action or a probe, simply doesn't implement it, and the engine never calls `Down` for it at all.
- **`plugin.Exporter`**: a step type whose `Up` creates something reachable from outside kevin implements `Export(ctx, *plugin.ExportRequest) (*plugin.ExportResult, error)`. `ExportResult` carries two independent fields: `Env map[string]string`, the environment variables an external command needs - this is what backs `kevin connect` - and `Out map[string]plugin.Value`, the same information in structured form, for another step's cross-scope `needs: ["setup.<name>"]` reference to read (see [Environment file: reading a setup step's value]({{< relref "/docs/configuring-an-environment#reading-a-setup-steps-value" >}})). `kevin connect` only ever reads `Env`; a cross-scope `needs` reference only ever reads `Out` - a plugin only needs to populate whichever one its step type actually supports, and can leave the other nil. `Out` uses the same `plugin.Value` interface as `Result.Outputs` (`plugin.String`, `plugin.Sensitive`, `plugin.StringMap` all apply). `Export` must not create or change anything, only report how to reach what `Up` already created.
- **`plugin.ToolProvider`**: a step type that offers one or more MCP tools implements `Tools() []plugin.ToolDef` and `CallTool(ctx, *plugin.ToolCallRequest) (*plugin.ToolCallResult, error)`. Each `ToolDef` names the tool and carries its `InputSchema`, an "object" JSON Schema document - it must not declare a `step` property, since kevin injects that one itself so an MCP client can name which running step instance the call targets. `CallTool` reads `req.Arguments` (JSON, the `step` property already stripped out) and returns a `ToolCallResult`: `Content` is JSON-marshaled and shown to the MCP client as structured content, and `IsError`/`ErrorMessage` report a tool-level failure without failing the RPC itself. See [The plugin protocol]({{< relref "plugin-protocol" >}}) for the wire method.

A step names this step type as `uses: "acme:widget"`, and a `plugins:` entry sets `cmd` to the binary. The engine launches the binary once per session and keeps it running, so a `Step` must be safe for concurrent use: the DAG can create several steps of one step type at the same time. One process serves every step type that its provider offers.

## Request data

Every call carries a `plugin.Env`, the same for every step in a session: the project name, the `.kevin` workspace path, the shared docker network, the CA certificate, the proxy and console addresses, the proxy environment variables, the environment domain, the relay address, the project directory (`ProjectDir`) that a step resolves a relative `with`-block path against, and the scope this step belongs to (`Scope`, `"setup"` or `"env"`).

A plugin that creates a docker resource should carry three labels, at increasing granularity - each value holding every segment up to its own tier, colon-separated: `kevin.project` (`Env.Project`), `kevin.scope` (`"<Env.Project>:<Env.Scope>"`), and `kevin.urn` (`"<Env.Project>:<Env.Scope>:<step name>"`). Docker's label filter is exact-match only, so each tier lets kevin's own reap query at that granularity in one call - `kevin.scope` in particular is what tells a "setup" step's resource apart from an "env" step's resource of the same name.

`Up` also carries `Deps`: a `map[string]map[string]plugin.Value` keyed by the name of every step named in this step's `needs` list, each holding that step's published `Outputs`. This is the same data a `${...}` expression in the `with` block reads. See [Environment file: cross-step values]({{< relref "/docs/configuring-an-environment#cross-step-values" >}}). `Down` carries only this step's own prior `Outputs`, replayed from state, so `Down` is self-sufficient even if the engine restarted since `Up` ran.

Every `plugin.Value` a step reads or publishes - in `Outputs`, `Deps`, or a `Detail` - is either a `plugin.String` or a `plugin.Sensitive{...}` wrapping one. Read one with `.Reveal()`. A `plugin.Sensitive` value's `String()` method returns `"[REDACTED]"`, so passing one to `fmt`/`log` by accident - including nested inside a map, slice, or struct - can't print the real content; only `.Reveal()` does. There's no separate "sensitive keys" list to remember to check.

See [The plugin protocol]({{< relref "plugin-protocol" >}}) for the full wire contract this rides on.

## Schema

`Step.Schema` returns the CUE that constrains the `with` block of that step type, or nil when the step type takes no configuration. A provider that takes its own configuration sets `Plugin.ConfigSchema` and `Plugin.Configure`; the engine calls `Configure` once, before any step of that provider runs. `Plugin.Icon` is an optional small PNG (48x48 or less) shown next to the provider's step types on the console; a provider that gives none shows a puzzle-piece placeholder instead.

Values returned in `Result.Outputs` are passed to every step that declares a `needs` edge on this one. `kevin plugin list` prints every builtin name, so a project can tell a builtin step type from a declared one.

A step that generates a secret - a password, an API token - wraps that `Outputs` entry in `plugin.Sensitive{...}` instead of `plugin.String(...)`. The engine carries that marking with the value as it propagates to dependent steps' `Deps` and never writes it to the console card or a log in full. This only covers structured data the engine itself handles: a plugin that prints its own secret to stdout/stderr defeats it, the same as it would with any other tool.

## Console card

`Result.Details` is the one channel that reaches a step's card on the console. `Route` and `ExposedPort` stay purely functional (proxy routing, port publishing) and don't put anything on the card themselves. Append a `plugin.Detail{Label, Value, Copyable, Href}` for each row a step's card should show; `Route` and `ExposedPort` each have a `Detail()` method that builds a sensible one to start from, if `Up` already returns either.

Wrap a `Detail`'s `Value` in `plugin.Sensitive{...}` when it carries a secret. The console masks it and never renders it as a link or a tooltip; a `Copyable` sensitive detail still exposes the real value to its copy-to-clipboard button, since that's the reveal path meant for a dev who needs the value, not a display path.

Reserved plugin namespaces (`builtin`, `cmd`, `core`, `docker`, `file`, `helm`, `http`, `k8s`, `kevin`, `kubectl`, `kubernetes`, `oci`, `official`, `std`) can't be used as a `plugins:` key, so a third-party plugin never reads as first-party.
