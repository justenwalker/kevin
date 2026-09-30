---
title: "The plugin protocol"
weight: 2
---

# The plugin protocol

kevin and a plugin talk over gRPC. The Go `plugin` package implements this protocol. A plugin in another language implements the service directly. The builtin step types use the same protocol: kevin starts them as `kevin plugin run <name>`.

One plugin process serves every step type of its provider.

## Methods

| Method | Called | Does |
|:-------|:-------|:-----|
| `Info` | Once, at start. | Returns the provider name and version, the CUE schema of the provider `config` block, an optional PNG icon (48x48 or smaller), and for each step type: the CUE schema of its `with` block, its kind (resource, action, or probe), whether it implements `Down` and `Export`, and its MCP tools. |
| `Configure` | Once, before the first step of the provider, if the environment file has a `config` block for it. | Receives the provider `config` block. |
| `Up` | For each step. | Creates the step and returns its outputs. The request names the step type. |
| `Down` | For each step, on teardown, if the step type implements it. | Removes the step. The request names the step type. |
| `Export` | For a `setup.<step>` need, `kevin do`, and the MCP `export_step` tool, if the step type implements it. | Returns values that describe how to reach what the step created, such as `kubeconfig` and `context`. Values can be marked sensitive. |
| `CallTool` | For an MCP tool call, if the step type declares tools. | Runs a tool against a running step. The request has a `step` property that kevin adds to the tool's schema. |

`Up` and `Down` stream their responses. One call carries log lines, progress, and the final result.

`Down` also runs for a step whose `Up` was still running when the run was canceled. That request carries no outputs, and the step can be absent or only partly created.

Each request has everything the plugin needs: the network name, the CA certificate, the proxy address, the workspace path, and the outputs of upstream steps. kevin has no callback service.

## Start sequence

1. Read the environment file and check it against the core schema.
2. Start each plugin that a step uses.
3. Call `Info` on each plugin, and collect the schemas.
4. Check each step's `with` block against the schema of its step type.
5. Call `Configure` on each plugin that has a `config` block.
6. Walk the DAG and call `Up` for each step. For a `setup.<step>` need, call that step's `Export` instead.

kevin calls `Up` only after step 4 succeeds, so an invalid environment file fails before a plugin creates anything.

The plugin processes run until the run ends.

## Related

- [Writing a plugin]({{< relref "writing-a-plugin" >}})
- [Cross-step values]({{< relref "/docs/concepts/cross-step-values" >}}): how outputs reach other steps.
