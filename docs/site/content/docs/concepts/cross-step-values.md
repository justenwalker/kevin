---
title: "Cross-step values"
description: "How a step reads another step's outputs, across scopes, and what happens when a plugin crashes."
weight: 3
---

# Cross-step values

A step publishes outputs, such as the address of a registry or the path of a kubeconfig file. Every step with that step in its `needs` gets them.

## Two paths to the outputs

A plugin gets every upstream output in its `Up` request, next to the `with` block. It also gets the containers of each upstream step, in a separate field (`UpRequest.Containers`). [`builtin:fault`]({{< relref "/docs/reference/steps/fault" >}}) uses this field to find the containers to change, with no `${...}` expression.

A `with` value can also read outputs with a `${...}` expression. kevin evaluates each expression as [CEL](https://github.com/google/cel-go) and replaces it with the result before the plugin gets the `with` block. The `needs` variable is a map from step name, to `out` or `system`, to key. `out` holds the plugin's outputs. `system` holds values kevin computes, such as relay addresses. They are separate so that a key kevin adds never conflicts with a key a plugin chose.

kevin evaluates the expressions for a step when its upstream steps are done, during the walk of the DAG. Schema validation happens once before the walk, and sees each `${...}` as a plain string. This works for any step type, builtin or third-party. See [CEL expressions]({{< relref "/docs/reference/cel-expressions" >}}).

A [step group]({{< relref "/docs/reference/environment-file#step-groups" >}}) computes its `outputs` the same way, over its members.

## Crossing scopes

An `env` step can need a `setup` step, with a `setup.` prefix: `needs: ["setup.cluster"]`. A bare name always means the same scope, so a name in both scopes is never ambiguous. The reverse is not allowed: `setup` steps exist independently of any `env` run.

`kevin run` does not start the `setup` scope, so kevin cannot get the value from `Up`. It calls the setup step's `Export` instead. The plugin is already running, because kevin starts every plugin that either scope uses. `Export` has no side effects and reports current state, so kevin calls it each time a step needs it, with no cache.

An expression reads the value as `${setup.<name>.out.<key>}`, not `${needs.setup.<name>...}`. The `needs` variable has three levels, and a setup reference needs a fourth. A separate `setup` variable with the same type as `needs` keeps both typed. It also keeps a same-scope step named `setup` readable at `needs.setup.out.<key>`.

## Sensitive values

A plugin can mark an output sensitive, such as a generated password. kevin then does not write it in full to logs or the console. The mark stays with the value through `Export` and `needs`. A `${...}` expression still reads the real value, because the result goes into a `with` block, not into a display. A step that shows such a value must mark it sensitive again.

## Plugin crashes

If an `Up` or `Down` call fails, the step fails, whether the plugin returned an error or its process exited. kevin then removes the steps that came up and any step whose `Up` was still running when the run was canceled, and deletes containers left with the project's labels. A crashed plugin shows as a gRPC `Unavailable` error, as in other go-plugin programs such as Terraform.

kevin does not restart a crashed plugin or resume the walk. Run `kevin run` again. A builtin step that creates a resource, such as a container or a cluster, names it from the project and step name, and its `Up` replaces or reuses what is there. A new run continues from the state that the failed run left. `builtin:exec` is the exception: kevin cannot know whether a command is safe to run twice.
