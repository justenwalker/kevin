---
title: "🔌 The plugin protocol"
weight: 2
---

# The plugin protocol

Every step type speaks the same protocol over gRPC. The engine has no privileged path for a step type that ships in this repository: every builtin step type compiles into the kevin binary the same way, and the engine starts it as `kevin plugin run <name>`. A third party writes a separate binary and gets the same protocol. One process serves every step type that its provider offers.

The service has six methods:

1. **`Info`** reports the provider name and version, the CUE schema for the provider's own config block, an optional small PNG icon (48x48 or less) shown next to the provider's step types on the console, and, for each step type it offers: a CUE schema for its `with` block, its `StepKind` (resource, action, or probe, used by the console and by docs), whether it implements `Down` and `Export`, and the MCP tools it offers, if any.
2. **`Configure`** delivers the provider's own `config` block, once, before any step of that provider runs.
3. **`Up`** creates one step and returns the outputs of that step. The request carries the step type beside the node name, so one process can dispatch to the right step type.
4. **`Down`** removes one step. The request also carries the step type. A step type implements `Down` only when its `Up` creates something that needs removing. `Info` reports, per step type, whether it does, so the engine's teardown walk skips the call entirely for a step type that doesn't, instead of calling it and getting a no-op.
5. **`Export`** reports what a step created, in two independent forms: `env`, the environment variables that let an external command reach it, such as `KUBECONFIG` for a Kubernetes cluster - this is what `kevin connect` injects into the shell it execs - and `out`, the same information in structured, `Value`-typed form (the same shape `Up`'s outputs use, so a value can be marked sensitive), for an env step's cross-scope `needs: ["setup.<name>"]` reference to read. A step type implements `Export` only when there's something to export. `Info` reports, per step type, whether it does, so a caller never needs to find out by calling `Export` and seeing what happens. `kevin connect` only ever reads `env`; a cross-scope `needs` reference only ever reads `out`.
6. **`CallTool`** runs one of a step type's declared tools against a running step instance, named by a `step` property the engine injects into the tool's schema. A step type implements `CallTool` only when it declares at least one tool via `Info`. See [Writing a plugin]({{< relref "writing-a-plugin" >}}) for `ToolProvider`.

`Up` and `Down` stream from the server. One call carries the log lines, the progress reports, and the final result. Thus the protocol needs no separate progress service.

The protocol has no callback service and no `GRPCBroker`. Everything a plugin needs is in the request message: the docker network name, the CA certificate, the proxy address, the workspace path, and the outputs of the upstream steps.

## Session startup

1. Read `kevin.cue` and unify the file with the core schema.
2. Start every declared plugin.
3. Call `Info` on each plugin and collect the CUE schemas.
4. Unify the `with` block of each step with the plugin's schema for that step.
5. Call `Configure` on each plugin that declares a `config` block, once, before any step of that plugin runs.
6. Walk the DAG and call `Up` for each step. A step whose `needs` names a setup step (`setup.<name>`) calls that setup step's `Export` instead - it is never walked or `Up`'d as part of this DAG.

A step runs only after step 4 succeeds. A bad environment file fails before the plugin creates a resource.

The plugin processes stay alive for the whole session. The engine stops them when the session ends.

See [Environment file: cross-step values]({{< relref "/docs/configuring-an-environment#cross-step-values" >}}) for how a step's outputs reach a downstream step, both in the plugin's own wire request and in `${...}` expressions inside `with`.
