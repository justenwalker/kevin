---
title: "kevin"
type: docs
---

# kevin

Most shell scripts that manage development environments have three problems:

1. The script runs steps in series even when the steps do not depend on each other.
2. A failure in the middle of the script leaves orphan containers.
3. They give no view of the traffic between the services under test.

While all of this is possible to do, it is challenging to do correctly.

`kevin` solves this with a step DAG, a plugin protocol, and a proxy that terminates TLS.

1. A DAG ensures that steps can be defined in any order, dependencies can be defined, and steps can be run in parallel.
2. A plugin protocol allows for extensibility and allows for the implementation of steps in any language.
3. A forward proxy that terminates TLS allows the developer to reach the services under test, without making permanent modifications to their development machine, such as editing the `/etc/hosts` file.
4. An egress proxy allows the developer to control the outbound traffic, and implement custom logic for the traffic between the services under test.
5. Hostname interception lets a `route` step stand a local container in for a real-world hostname (e.g. `s3.amazonaws.com`), so code that talks to a third-party service can be pointed at a local fake with no code change and no `/etc/hosts` edit.

You write a `kevin.cue` file describing the pieces your local environment needs and how they depend on each other. kevin then brings the pieces up in dependency order, passing each step's output (an address, a kubeconfig path) to the steps that need it. There's no state file to get out of sync: tearing the environment down works from what's actually running, so it's safe even after a crash mid-run.

## Comparison

See [kevin vs. other tools]({{< relref "docs/comparison" >}}) for what's actually alike and what's actually different, tool by tool.

## Start here

- **[Quickstart]({{< relref "docs/quickstart" >}})**: build kevin, run a real environment, and see the proxy, the console, and egress control in action.
- **[kevin vs. other tools]({{< relref "docs/comparison" >}})**: how kevin compares to shell scripts, Docker Compose, Terraform, Tilt, and Garden.
- **[Guides]({{< relref "docs/guides" >}})**: how to use the proxy, the CA, Kubernetes clusters, and deploying workloads.
- **[Concepts]({{< relref "docs/concepts" >}})**: why the DAG engine, the proxy, and the CA are shaped the way they are.
- **[Reference]({{< relref "docs/reference" >}})**: the `with` block config for every builtin step type.
- **[Extending kevin]({{< relref "docs/extending" >}})**: write a new step type as a plugin.

Source: [github.com/justenwalker/kevin](https://github.com/justenwalker/kevin)
