---
title: "Writing a plugin"
weight: 1
---

# Writing a plugin

A plugin is a program that offers one or more step types. This guide writes one in Go with the `github.com/justenwalker/kevin/plugin` package. [`cmd/kevin-plugin-echo`](https://github.com/justenwalker/kevin/tree/main/cmd/kevin-plugin-echo) is a complete example with three step types and a provider `config` block.

## Write the plugin

A step type implements `plugin.Step`. `main` passes the step types to `plugin.Serve`:

```go
package main

import (
    "context"

    "github.com/justenwalker/kevin/plugin"
)

type widgetStep struct{}

func (widgetStep) Schema() []byte { return []byte(`#Config: image!: string`) }

func (widgetStep) Kind() plugin.StepKind { return plugin.StepKindResource }

func (widgetStep) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
    out.Log("stdout", "creating "+req.Step)
    return &plugin.Result{Outputs: map[string]plugin.Value{"endpoint": plugin.String("...")}}, nil
}

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

## Use the plugin

Build the binary, then add it to an environment file:

```cue
plugins: acme: cmd: "./bin/kevin-plugin-acme"

env: api: {
    uses: "acme:widget"
    with: image: "nginx"
}
```

The plugin name cannot be a [reserved name]({{< relref "/docs/concepts/scopes-and-providers#reserved-names" >}}).

Check it with `kevin validate`, then start it with `kevin run`.

kevin starts the plugin once for each run and keeps it running. The DAG can run several steps of one step type at the same time, so each `Step` must be safe for concurrent use.

## Define the schema

`Schema` returns CUE that defines `#Config`, the `with` block of the step type. Return `nil` if the step type has no configuration. kevin checks each step's `with` block against it before anything runs.

For a provider-wide `config` block, set `Plugin.ConfigSchema` and `Plugin.Configure`. kevin calls `Configure` once, before the first step of the provider.

`Plugin.Icon` is an optional PNG, 48x48 or smaller, that the console shows next to the provider's step types.

## Choose a step kind

`Kind` classifies the step type for the console and the documentation. It does not change how kevin runs the step.

| Kind | `Up` |
|:-----|:-----|
| `plugin.StepKindResource` | Creates something that `Down` removes. Most step types. |
| `plugin.StepKindAction` | Changes something it does not own, such as applying a manifest to a cluster. |
| `plugin.StepKindProbe` | Creates nothing. Checks that something is ready. |

## Implement teardown

If `Up` creates something, implement `plugin.Downer`:

```go
Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error
```

`Down` must be safe to call for a step that never started or is already gone. kevin keeps no state file, so find what to remove from what exists, for example by container labels (see [Request data](#request-data)). `req.Outputs` has the step's outputs when the same kevin process ran its `Up`. After `kevin teardown` in a new process, it can be empty, so do not depend on it.

A step type with nothing to remove does not implement `Downer`, and kevin does not call `Down` for it.

## Implement export

If `Up` creates something that tools outside kevin can reach, implement `plugin.Exporter`:

```go
Export(ctx context.Context, req *plugin.ExportRequest) (*plugin.ExportResult, error)
```

`ExportResult.Out` has the same type as `Result.Outputs`. `Export` must report how to reach what exists, and must not create or change anything. kevin calls it for a `setup.<step>` need, for `kevin do`, and for the MCP `export_step` tool.

## Add MCP tools

A step type can give coding agents tools through the kevin MCP server. Implement `plugin.ToolProvider`:

```go
Tools() []plugin.ToolDef
CallTool(ctx context.Context, req *plugin.ToolCallRequest) (*plugin.ToolCallResult, error)
```

Each `ToolDef` has a name and an `InputSchema`, a JSON Schema of type `object`. Do not declare a `step` property: kevin adds it, so the client can name the step the call is for.

`CallTool` reads `req.Arguments`, JSON without the `step` property. It returns a `ToolCallResult`: kevin sends `Content` to the client as structured JSON. To report a failure of the tool, set `IsError` and `ErrorMessage` instead of returning an error.

## Request data

Every request has a `plugin.Env`, the same for every step in a run:

| Field | Value |
|:------|:------|
| `Project` | Project name. |
| `Scope` | `"setup"` or `"env"`. |
| `Workspace` | The project's `.kevin` state directory. |
| `ProjectDir` | The project directory. Resolve relative paths in `with` against it. |
| `Network` | Name of the project network. |
| `Engine` | `"docker"` or `"podman"`. |
| `CAPath` | Path of the CA certificate file. |
| `HTTPProxyAddr` | Address of the proxy. |
| `ConsoleAddr` | Address of the console. |
| `ProxyEnv` | Proxy environment variables, such as `HTTPS_PROXY` and `NO_PROXY`. |
| `Domain` | The environment domain. |
| `Relay` | Address of the relay on the project network. |
| `RelaySOCKS5Addr` | Host address of the relay's SOCKS5 server. |

`UpRequest.Deps` has the `Outputs` of each step in `needs`, by step name. These are the values that `${needs...}` expressions read.

Label every container or other resource you create, so kevin can find and remove it after a crash:

| Label | Value |
|:------|:------|
| `kevin.project` | `Env.Project` |
| `kevin.scope` | `<Env.Project>:<Env.Scope>` |
| `kevin.urn` | `<Env.Project>:<Env.Scope>:<step name>` |

## Mark secrets as sensitive

Every `plugin.Value` is a `plugin.String` or a `plugin.Sensitive`. Wrap a generated secret, such as a password, in `plugin.Sensitive{...}`. kevin keeps the mark when it passes the value to other steps, and does not write the value in full to logs or the console.

`Sensitive.String()` returns `"[REDACTED]"`, also inside maps, slices, and structs, so printing a value by mistake does not show it. Call `.Reveal()` to read the value.

kevin cannot protect a secret that your plugin prints to its own output.

## Add rows to the console

`Result.Details` is the list of rows on the step's console card, shown in its detail dialog's Details tab. Add a `plugin.Detail{Label, Value, Copyable, Href}` for each row. `Route` and `ExposedPort` each have a `Detail()` method that returns a row for them.

If a `Detail`'s `Value` is `plugin.Sensitive`, the console masks it and does not show it as a link or tooltip. If it is also `Copyable`, the copy button copies the real value.

The same dialog's Inputs and Outputs tabs need nothing from the plugin: Inputs shows the step's resolved `with:` config, and Outputs shows `Result.Outputs`, both masked the same way a sensitive `Detail` is.

Inputs sensitivity is usually inferred: a field is masked when it embeds a `${needs...}`/`${setup...}` reference to an already-sensitive output. That inference has nothing to trace for a field whose value is always secret regardless of source - a literal typed directly into `kevin.cue`, say. Mark such a field `@sensitive()` in your schema.cue, and the console and MCP server mask it unconditionally:

```cue
#Config: {
	// password authenticates against the upstream service.
	password?: string @sensitive()
}
```

## Stream a command's output

If a step runs a long command, send its output to the console as it runs:

```go
cmd := exec.CommandContext(ctx, "some-tool", args...)
cmd.Stdout = plugin.NewLineWriter(out, "stdout")
cmd.Stderr = plugin.NewLineWriter(out, "stderr")
return cmd.Run()
```

`NewLineWriter` sends each complete line to `out.Log`.

## Test the plugin

A `Step` is a Go interface, so you can call `Up`, `Down`, and `Export` in a test with no gRPC. Pass a small `plugin.Emitter`:

```go
type capture struct{ logs []string }

func (c *capture) Log(_, text string)               { c.logs = append(c.logs, text) }
func (c *capture) Progress(label string, _, _ int64) {}

func TestUp(t *testing.T) {
    out := &capture{}
    result, err := widgetStep{}.Up(t.Context(), &plugin.UpRequest{
        Step:   "api",
        Config: []byte(`{"image":"nginx"}`),
    }, out)
    require.NoError(t, err)
    assert.Equal(t, "...", result.Outputs["endpoint"].Reveal())
}
```

The tests of `cmd/kevin-plugin-echo` (`echo_test.go`, `probe_test.go`, `config_test.go`) use this pattern.

To test the binary end to end, build it and run `kevin validate` and `kevin run` with an environment file that uses it.

## Next steps

- [Publishing a plugin]({{< relref "publishing-a-plugin" >}}): package, sign, and share the plugin.
- [The plugin protocol]({{< relref "plugin-protocol" >}}): the gRPC service under the Go package.
