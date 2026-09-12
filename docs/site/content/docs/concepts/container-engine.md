---
title: "Container Engine"
description: "Why kevin shells out to the engine's CLI instead of importing an SDK, docker vs. podman, and how labels replace a state file."
weight: 9
---

# Container Engine

kevin drives a container engine by running its CLI and parsing the output, never by importing an SDK. Which engine is a choice about this machine, not the project - `kevin.cue` has no field for it. Pick one with the `--engine` flag or the `KEVIN_ENGINE` environment variable (`"docker"` or `"podman"`); with neither set, kevin auto-detects by probing each engine's daemon, preferring docker when both answer. Every builtin step and the engine's own network/cleanup logic go through the same [`cri.Runtime`](https://github.com/justenwalker/kevin/blob/main/internal/cri/cri.go) contract regardless of which engine is selected, so the rest of this page applies to both - "docker" below names the default, not the only option.

The reason for shelling out is the size of the dependency. `github.com/docker/docker` pulls in a large tree for a small number of calls, and podman has no comparable Go client kevin would want to import either. The cost of the choice is the parse of the command output, and a runtime dependency on the `docker` or `podman` binary.

Every container carries three labels at increasing granularity - a materialized path, each value holding every segment up to its own tier: `kevin.project` (`"<project>"`), `kevin.scope` (`"<project>:<scope>"`), and `kevin.urn` (`"<project>:<scope>:<step>"`). Both engines' label filters are exact-match only, with no prefix or wildcard, so each tier is its own label: `kevin.project` finds every resource of a project in one query, `kevin.scope` finds every resource of one scope in one query, without a "setup" step and an "env" step of the same name being confused for each other. The engine lists the containers of the project after it removes the steps, and deletes whatever is left - except a container whose `kevin.scope` names the other scope and is still live, since setup and env share one project network. There is no state file: a label survives a crash, and a file can go stale.

The engine creates the shared network before the DAG runs and removes it after. A container joins that network with a network alias equal to the step name, thus one step reaches another by step name. The network is dual-stack (`docker network create --ipv6`, or podman's equivalent): a container gets both an IPv4 and, where the daemon assigns one, an IPv6 address, and the relay answers DNS and captures egress on whichever families it finds addresses in - see [Transparent capture]({{< relref "/docs/concepts/relay#transparent-capture" >}}).

## Podman

Podman support (`internal/podman`) mirrors the docker engine's CLI wrapper method-for-method - podman's CLI is docker-compatible by design. A [`builtin:kind`]({{< relref "/docs/reference/steps/kind" >}}) cluster's own node containers can run on podman too, through kind's own `KIND_EXPERIMENTAL_PROVIDER` switch, upstream-labeled experimental - kevin sets it automatically when podman is the selected engine.
