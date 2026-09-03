---
title: "Cross-step values"
description: "How a step reads another step's outputs, across scopes, and what happens when a plugin crashes."
weight: 3
---

# Cross-step values

Every step type speaks the same protocol over gRPC. See [The plugin protocol]({{< relref "/docs/extending/plugin-protocol" >}}) for the RPCs and the session-startup sequence that calls them.

Session startup's per-step `with`-block validation (step 4 of that sequence) scales linearly with step count, not quadratically, on the measurements taken so far, at 1, 10, 100, and 1000 steps. Re-check that scaling before any change to how a step's `with` block gets validated.

## Cross-step values

A step publishes outputs. Every step that declares a `needs` edge on that step receives the outputs. A downstream step reads a value such as the endpoint of a registry, or the path of a kubeconfig file.

A step's own plugin code always gets every upstream output through the wire request. The engine passes it beside the `with` block, not through it. But a `with` value itself can also reference one, using `${cel-expression}`: any string in the `with` block, at any depth, that contains `${...}` gets that expression evaluated, against a `needs` variable shaped `map[string]map[string]map[string]string`, keyed by upstream step name, then by `out` (that step's own plugin-authored outputs) or `system` (values kevin computes itself, kept apart so a kevin-computed key can never collide with one a plugin chose for its own output). The same expression can also read `env.<VAR>`, the kevin process's own environment variables; referencing an unset one errors, so `has(env.VAR) ? env.VAR : "default"` is the idiom for an optional one. The result is spliced back into the surrounding text before the plugin ever sees it. A step whose `with` block never uses `${` pays no cost; there is no other change to what the plugin receives.

This is implemented with [CEL](https://github.com/google/cel-go), called once per step at exactly the point in the DAG walk where the upstream outputs for that step are already assembled, so it needs no separate resolution phase and no reordering of validate-then-walk: schema validation of the `with` block still happens once, globally, before the walk starts, and only ever sees the `${...}` placeholder as a plain string. The mechanism itself is generic: any step type's `with` block can use it, builtin or third-party. See [CEL expressions]({{< relref "/docs/reference/cel-expressions" >}}) for the full syntax reference.

### Crossing scopes with `needs`

An `env` step's `needs` may name a `setup` step, one-way only - never the reverse, since `setup` is provisioned independently of any `env` run. The entry carries a literal `setup.` prefix, e.g. `needs: ["setup.cluster"]`: a bare name always means same-scope, so there is never a fallback search into the other scope and never any ambiguity if the same name happens to exist in both.

Because a plain `kevin run` never brings the setup scope up in that process, this dependency can't be satisfied by walking the DAG the way a same-scope one is. It's resolved by calling the setup step's plugin `Export` RPC instead - the setup step's plugin is already running (every plugin either scope references gets started regardless of which one is executing), so no extra process launch is needed, and `Export` is specced as a cheap, side-effect-free, always-live query, so kevin calls it fresh every time a dependent step runs rather than caching it.

Reading the resolved value back uses a separate top-level `setup` CEL variable, not a nested `needs.setup...` path: `${setup.<name>.out.<key>}`. This is deliberate, not cosmetic - `needs`'s CEL type is a uniform three-level map (step name → `out`/`system` → key), and a cross-scope reference needs one more level (the setup step's own name) that can't coexist with that uniform shape in the same variable without losing static typing. A sibling `setup` variable, with the exact same type as `needs`, sidesteps that entirely and, as a side effect, means a same-scope step literally named `setup` stays reachable at the ordinary `needs.setup.out.x` with zero ambiguity against the new `setup.<name>.out.x`.

`Export`'s `out` field carries the same value type `Up`'s outputs do, so a plugin can mark an exported value sensitive - a generated password from a `setup` step, say - and that flag survives all the way to the step that named it in `needs`, the same as a same-scope sensitive output would. CEL rendering itself still discards sensitivity either way (a value substituted into a rendered `with` string has no way to carry a flag); the flag is preserved end to end everywhere except inside that substitution.

## Crash recovery

Any `Up`/`Down` RPC failure fails that step, the same whether the plugin returned an ordinary error or the process crashed out from under the call. The engine removes whatever came up (it keeps an accurate record of every step that finished before the failure, so `Down` still runs for those), followed by a docker-label reap sweep, same as any other step failure.

A crash specifically surfaces as its own recognizable error, an `Unavailable` gRPC transport error - the same heuristic other go-plugin consumers such as Terraform use to recognize a dead process - rather than an opaque wrapped transport error, so the failure is recognizable for what it is.

There's no restart-in-place: kevin doesn't try to relaunch the crashed plugin process and resume the walk mid-flight. The actual recovery path is re-running `kevin run`. Every builtin step's `Up` is idempotent on its deterministic `project`+`step` name - some delete then create, others are apply-style by construction (see [Docker]({{< relref "/docs/concepts/docker" >}})) - specifically so that a fresh run safely picks up wherever a crashed one left off, without kevin needing any state file or restart machinery to make that safe.
