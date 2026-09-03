---
title: "Docker"
description: "Why kevin shells out to the docker CLI instead of importing the SDK, and how labels replace a state file."
weight: 9
---

# Docker

kevin runs the `docker` command and parses the JSON output. kevin does not import the Docker SDK.

The reason is the size of the dependency. `github.com/docker/docker` pulls in a large tree for a small number of calls. The cost of the choice is the parse of the command output, and a runtime dependency on the `docker` binary.

Every container carries three labels at increasing granularity - a materialized path, each value holding every segment up to its own tier: `kevin.project` (`"<project>"`), `kevin.scope` (`"<project>:<scope>"`), and `kevin.urn` (`"<project>:<scope>:<step>"`). Docker's label filter is exact-match only, with no prefix or wildcard, so each tier is its own label: `kevin.project` finds every resource of a project in one query, `kevin.scope` finds every resource of one scope in one query, without a "setup" step and an "env" step of the same name being confused for each other. The engine lists the containers of the project after it removes the steps, and deletes whatever is left - except a container whose `kevin.scope` names the other scope and is still live, since setup and env share one project network. There is no state file: a label survives a crash, and a file can go stale.

The engine creates the shared network before the DAG runs and removes it after. A container joins that network with a network alias equal to the step name, thus one step reaches another by step name.
