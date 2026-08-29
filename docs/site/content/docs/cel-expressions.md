---
title: "🧮 CEL expressions"
weight: 4
---

# CEL expressions

Any string in a step's `with` block, at any depth, that contains `${...}`
gets the text inside the braces evaluated as a
[CEL](https://cel.dev) (Common Expression Language) expression, and the
result spliced back into the surrounding text. A string with no `${` is
never touched. This page documents the three variables kevin exposes to that
expression; for the CEL language itself (operators, `has()`, ternary `? :`,
string/list/map methods), see the
[CEL language definition](https://github.com/google/cel-spec/blob/master/doc/langdef.md).

```cue
with: {
    kubeconfig: "${needs.cluster.out.kubeconfig}"
    registry:   "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
}
```

A string can contain more than one `${...}` expression; each is evaluated
and spliced in independently, left to right.

## Variables

| Name | Type | Description |
|:-----|:-----|:-------------|
| `needs` | `map[string]map[string]map[string]string` | Keyed by upstream step name (must be listed in this step's `needs`), then by sub-namespace (`out` or `system`), then by key. |
| `setup` | `map[string]map[string]map[string]string` | Same shape as `needs`, but for a cross-scope `needs: ["setup.<name>"]` reference - keyed by the setup step's name, with no `setup.` prefix. Empty when rendering a `setup`-scope step itself. |
| `env` | `map[string]string` | The kevin process's own environment variables, keyed by name. |

### `needs.<step>.out.<key>`

A value from `<step>`'s own `Result.Outputs`, the plugin-authored outputs
that step returned from `Up`. See
[Cross-step values]({{< relref "/docs/configuring-an-environment#cross-step-values" >}}).

```cue
kubeconfig: "${needs.cluster.out.kubeconfig}"
```

### `needs.<step>.system.<key>`

A value kevin computes itself for `<step>`, kept in a separate namespace so
it can never collide with a key the plugin chose for its own output,
currently `expose_<name>`/`forward_<name>` for that step's `ExposedPort`
entries. See
[Cluster tunnel]({{< relref "/docs/concepts/architecture#cluster-tunnel" >}}).

```cue
tcp: address: "${needs.cluster.system.expose_postgres}"
```

### `setup.<name>.out.<key>`

A value from a `setup`-scope step's `Export` result, for an `env` step
whose `needs` names it as `setup.<name>`. Unlike `needs.<step>.out.<key>`,
this isn't a same-process `Up` result - a plain `kevin run` never brings
the `setup` scope up, so kevin calls the setup step's `Export` RPC to get
it, fresh every time. Only `env`-scope steps can use this; `setup` steps
can't depend on `env`. See
[Cross-step values]({{< relref "/docs/concepts/architecture#cross-step-values" >}}).

```cue
setup: cluster: {uses: "builtin:kind"}
env: deploy: {
    uses:  "builtin:kubectl"
    needs: ["setup.cluster"]
    with:  kubeconfig: "${setup.cluster.out.KUBECONFIG}"
}
```

A same-scope step literally named `setup` is unaffected - it stays
reachable at `needs.setup.out.<key>`, a different variable entirely from
the top-level `setup.<name>.out.<key>` above.

### `env.<VAR>`

The named environment variable from kevin's own process environment.

```cue
registry: "${env.REGISTRY_HOST}"
```

## Errors

Every error below fails the step's `Up`/`Down` before the plugin ever sees
the `with` block.

| Cause | Example | Message mentions |
|:------|:--------|:------------------|
| `<step>` isn't listed in this step's `needs`, or has no such `out`/`system` key | `${needs.other.out.x}` | the step name |
| `<VAR>` isn't set in kevin's environment | `${env.MISSING}` | the step name |
| The expression doesn't evaluate to a string (CEL result must be `string`) | `${1 + 1}` | `must evaluate to a string` |
| `${` with no matching `}` | `${needs.cluster.out.x` | the unbalanced marker |
| The text inside `${...}` isn't valid CEL | `${needs.}` | the CEL compile error |

A reference to an unset `env` variable errors rather than substituting an
empty string, the same as an unknown `needs` key. Use `has()` for a
variable that might not be set:

```cue
registry: "${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : \"localhost:5000\"}"
```
