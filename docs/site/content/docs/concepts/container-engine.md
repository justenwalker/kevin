---
title: "Container engine"
description: "Why kevin runs the engine's command instead of using an SDK, docker and podman, and how labels replace a state file."
weight: 9
---

# Container engine

kevin controls containers by running the `docker` or `podman` command and parsing its JSON output. It does not import an SDK.

## Why a command, not an SDK

The Docker Go module brings a large dependency tree for the few calls kevin makes, and Podman has no comparable Go client. The cost is that kevin parses command output, and needs the command installed.

## Selecting an engine

The engine is a setting of your machine, not of the project, so the environment file has no field for it. Use `--engine` or `KEVIN_ENGINE`, with `docker` or `podman`. With neither, kevin uses the first engine whose daemon answers, and prefers docker.

Both engines implement the same internal interface, `cri.Runtime`. Podman's command is compatible with Docker's, so the two implementations match method for method. With `--engine podman`, a [`builtin:kubernetes`]({{< relref "/docs/reference/steps/kubernetes" >}}) cluster runs on Podman. The `kind` driver does this through kind's experimental `KIND_EXPERIMENTAL_PROVIDER` setting, which kevin sets.
## Labels instead of a state file

Each container has three labels. Each value includes the value of the label before it:

| Label | Value |
|:------|:------|
| `kevin.project` | `<project>` |
| `kevin.scope` | `<project>:<scope>` |
| `kevin.urn` | `<project>:<scope>:<step>` |

Label filters match exact values only, so each level needs its own label. `kevin.project` finds every resource of a project in one query. `kevin.scope` finds every resource of one scope, so a `setup` step and an `env` step with the same name stay separate.

After kevin removes the steps, it lists the project's containers and deletes what is left. It keeps a running container of the other scope, because `setup` and `env` share one network.

A label stays on the container after a crash. A state file can go out of date. This is why kevin can clean up after a crash with no record of what it created.

## The project network

kevin creates one network for the project before the first step, and removes it after the last. Each container joins it with the step name as a network alias, so steps reach each other by name. The network has IPv4 and IPv6. The relay answers DNS and handles traffic for each address family it has an address in. A Kubernetes node keeps the network its cluster tool creates and also joins this one. The project network carries the node's default route, so the node's egress leaves through it. See [Relay]({{< relref "/docs/concepts/relay" >}}).
