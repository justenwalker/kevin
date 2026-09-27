---
title: "CEL expressions"
weight: 1
description: "The ${...} syntax and variables (needs, setup, env, project, vars) available inside a step's with block."
---

# CEL expressions

kevin evaluates each `${...}` in a string in a step's `with` block, a plugin's `config` block, a group's `outputs`, or a command's `run` as a [CEL](https://cel.dev) expression, and replaces it with the result. A string can hold more than one expression, spliced into the surrounding text as a string. A string with no `${` does not change.

A field whose entire value is one bare `${...}` marker, with no surrounding text, takes on that expression's own type instead - a number, a bool, a list, or a struct, not only a string. This only matters for `vars`, where a declared variable can carry a type other than string; every other variable (`needs`, `setup`, `env`, `project`) is always a string.

This page lists the variables kevin provides. For the language itself (operators, `has()`, `? :`, string methods), see the [CEL language definition](https://github.com/google/cel-spec/blob/master/doc/langdef.md).

```cue
with: {
    kubeconfig: "${needs.cluster.out.kubeconfig}"
    registry:   "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
}
```

## `needs`

`needs.<step>.<namespace>.<key>`. `<step>` must be in this step's `needs`.

| Namespace | Example | Value |
|:----------|:--------|:------|
| `out` | `${needs.cluster.out.kubeconfig}` | An output of `<step>`. Each step type's reference page lists its outputs. |
| `system` | `${needs.db.system.forward_postgres}` | A value kevin computes for `<step>`. See [System values](#system-values). |

Inside a [step group]({{< relref "/docs/reference/environment-file#step-groups" >}}), `<step>` names a member by its bare name, not `<group>.<member>`. A group's `outputs` use the same syntax over the group's members.

### System values

| Key | Value |
|:----|:------|
| `expose_<name>` | For an `expose` entry that goes through the relay: the relay address, as `socks5://<relay>/<host:port>`. A [`builtin:wait`]({{< relref "/docs/reference/steps/wait" >}}) `tcp` check accepts this form. |
| `forward_<name>` | For the same entry: a `127.0.0.1:<port>` address that any TCP or UDP client can connect to. |

## `setup`

`setup.<name>.out.<key>`. An output of a `setup` step, for an `env` step with `setup.<name>` in its `needs`. Only `env` steps and commands can use `setup`.

```cue
setup: cluster: {uses: "builtin:kind"}
env: deploy: {
    uses:  "builtin:kubectl"
    needs: ["setup.cluster"]
    with:  kubeconfig: "${setup.cluster.out.kubeconfig}"
}
```

A step in the same scope that is named `setup` is still `needs.setup.out.<key>`.

## `env`

`env.<VAR>`. An environment variable of the `kevin` process. An unset variable is an error. Use `has()` to give a default:

```cue
registry: "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
```

## `project`

`project.<key>`. A value for the whole project:

| Key | Value |
|:----|:------|
| `dir` | Absolute path of the project directory (the directory holding `kevin.cue`). |
| `root_cert` | Host path of kevin's root CA certificate file. |
| `ca_cert` | Host path of this project's intermediate CA certificate file. |
| `ca_key` | Host path of this project's intermediate CA private key file. |
| `http_proxy_addr` | `host:port` of kevin's own HTTP(S) proxy, reachable from the host. |
| `relay` | Address of the relay container on the container network. |

Use these for a tool that takes a CA file or proxy address only as a flag:

```cue
up: command: [
    "curl", "--cacert", "${project.root_cert}",
    "--proxy", "${project.http_proxy_addr}",
    "https://internal.example.com",
]
```

## `vars`

`vars.<name>`. A [`variables`]({{< relref "/docs/reference/environment-file#variables" >}}) entry's resolved value - a var-file, a `KEVIN_VAR_<NAME>` environment variable, a `--var` argument, or the entry's own `default`. `vars` is the only one of these variables that isn't always a string: a variable with no declared `type` is a string, one with a `type` resolves to that type.

```cue
variables: {
    region:   {default: "us-east-1"}
    replicas: {type: int, default: 3}
}
env: app: with: {
    env:      REGION: "${vars.region}"
    replicas: "${vars.replicas}"
}
```

Available in a step's `with` block and a plugin's `config` block. A `variables` entry with `sensitive: true` is always redacted wherever the console or MCP server shows a field that reads it, the same as a field reading an already-sensitive output.

## Errors

Each error fails the step before the step starts. `kevin validate` also reports a `needs` or `setup` reference to a step that is not in `needs`, and a `vars` reference to a name `variables` does not declare.

| Cause | Example | Message mentions |
|:------|:--------|:------------------|
| `<step>` isn't listed in this step's `needs`, or has no such `out`/`system` key | `${needs.other.out.x}` | the step name |
| `<VAR>` isn't set in kevin's environment | `${env.MISSING}` | the step name |
| `<key>` isn't one of `project`'s known keys | `${project.no_such_key}` | the step name |
| `<name>` isn't declared in `variables`, or has no value from any source and no `default` | `${vars.no_such_key}` | the variable name |
| A bare marker's resolved value doesn't satisfy the field's own schema type | a `string`-typed variable filling a field the plugin's schema types `int` | the field name |
| A non-string result is spliced into surrounding text | `"count-${1 + 1}"` | `must evaluate to a string` |
| `${` with no matching `}` | `${needs.cluster.out.x` | the unbalanced marker |
| The text inside `${...}` isn't valid CEL | `${needs.}` | the CEL compile error |
