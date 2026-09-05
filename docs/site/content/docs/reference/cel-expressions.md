---
title: "CEL expressions"
weight: 1
description: "The ${...} syntax and variables (needs, setup, env, project) available inside a step's with block."
---

# CEL expressions

Any `${...}` inside a step's `with` block string is evaluated as
[CEL](https://cel.dev) and spliced back in; a string with no `${` is
untouched, and more than one `${...}` per string is fine, evaluated left to
right. This page covers the four variables kevin exposes; for the language
itself (operators, `has()`, ternary `? :`, string/list/map methods), see the
[CEL language definition](https://github.com/google/cel-spec/blob/master/doc/langdef.md).

```cue
with: {
    kubeconfig: "${needs.cluster.out.kubeconfig}"
    registry:   "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
}
```

## Variables

Four top-level scopes: [`needs`](#needs), [`setup`](#setup), [`env`](#env),
[`project`](#project).

## `needs`

`needs.<step>.<ns>.<key>` - `<step>` must be listed in this step's `needs`.

| Namespace | Example | Value |
|:----------|:--------|:------|
| `out` | `${needs.cluster.out.kubeconfig}` | `<step>`'s `Result.Outputs` (plugin-authored). See [Cross-step values]({{< relref "/docs/environment-file#cross-step-values" >}}). |
| `system` | `${needs.cluster.system.expose_postgres}` | A value kevin computes itself for `<step>`, namespaced apart from `out` so it never collides with a plugin-chosen key - currently `expose_<name>`/`forward_<name>` for that step's `ExposedPort` entries. See [Cluster tunnel]({{< relref "/docs/concepts/relay#cluster-tunnel" >}}). |

Inside a [step group]({{< relref "/docs/environment-file#step-groups" >}}),
`<step>` can also name a sibling member by that member's own bare name - the
group joins the edge internally, never spelled out as `<group>.<member>`. A
group's own `outputs` block uses this exact same syntax, scoped to its own
members, to compute what the group exposes to the outside.

## `setup`

`setup.<name>.out.<key>` - a `setup`-scope step's `Export` output, for an
`env` step whose `needs` names it `setup.<name>`. Resolved via `Export`,
not `Up` - a plain `kevin run` never brings the `setup` scope up, so this
is fetched fresh every time. `env`-scope steps only; a `setup` step can't
depend on `env`. See
[Cross-step values]({{< relref "/docs/environment-file#cross-step-values" >}}).

```cue
setup: cluster: {uses: "builtin:kind"}
env: deploy: {
    uses:  "builtin:kubectl"
    needs: ["setup.cluster"]
    with:  kubeconfig: "${setup.cluster.out.KUBECONFIG}"
}
```

A same-scope step literally named `setup` is unaffected: it stays
`needs.setup.out.<key>`, a different variable from `setup.<name>.out.<key>`.

## `env`

`env.<VAR>` - the named variable from kevin's own process environment.

```cue
registry: "${env.REGISTRY_HOST}"
```

## `project`

`project.<key>` - a fixed project-level constant, computed once per
session, independent of any step:

| Key | Value |
|:----|:------|
| `dir` | Absolute path of the project directory (the directory holding `kevin.cue`). |
| `root_cert` | Host path of kevin's root CA certificate file. |
| `ca_cert` | Host path of this project's intermediate CA certificate file. |
| `ca_key` | Host path of this project's intermediate CA private key file. |
| `http_proxy_addr` | `host:port` of kevin's own HTTP(S) proxy, reachable from the host. |

For a tool that only takes these as a flag, not via
`SSL_CERT_FILE`/`HTTP_PROXY`-style env vars:

```cue
up: command: [
    "curl", "--cacert", "${project.root_cert}",
    "--proxy", "${project.http_proxy_addr}",
    "https://internal.example.com",
]
```

## Errors

Every error below fails the step's `Up`/`Down` before the plugin ever sees
the `with` block.

| Cause | Example | Message mentions |
|:------|:--------|:------------------|
| `<step>` isn't listed in this step's `needs`, or has no such `out`/`system` key | `${needs.other.out.x}` | the step name |
| `<VAR>` isn't set in kevin's environment | `${env.MISSING}` | the step name |
| `<key>` isn't one of `project`'s known keys | `${project.no_such_key}` | the step name |
| The expression doesn't evaluate to a string (CEL result must be `string`) | `${1 + 1}` | `must evaluate to a string` |
| `${` with no matching `}` | `${needs.cluster.out.x` | the unbalanced marker |
| The text inside `${...}` isn't valid CEL | `${needs.}` | the CEL compile error |

A reference to an unset `env` variable errors rather than substituting an
empty string, the same as an unknown `needs` key. Use `has()` for a
variable that might not be set:

```cue
registry: "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
```
