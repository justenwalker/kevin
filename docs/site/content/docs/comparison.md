---
title: "Kevin vs. Other Tools"
weight: 100
---

# kevin vs. other tools

kevin combines four things: an environment that exists only while you use it, a DAG that runs independent steps in parallel, a proxy that terminates TLS, controls egress, and can intercept real hostnames, and a plugin protocol that any language can implement. This page compares kevin with other tools, from the most similar to the least.

## Garden

[Garden](https://garden.io) is the most similar. A Garden project is a [graph of actions](https://docs.garden.io/reference/glossary#action-graph) run by [providers](https://docs.garden.io/reference/providers), the same model as kevin's steps and plugins, and it is for the same job: development and test environments, locally or in CI.

The differences are size and traffic control. Garden's [configuration](https://docs.garden.io/using-garden/configuration-overview) has a project config, several action types (`Build`, `Deploy`, `Run`, `Test`), providers, and workflows. kevin has one file with a map of steps. Garden has no proxy: no TLS termination between services, no egress allow list, no log of requests, and no hostname interception.

If you use Garden and do not need traffic control, Garden covers the same job. kevin fits a small environment of containers and Kubernetes where you want to see and control the traffic.

## Terraform or OpenTofu

Terraform's [dependency graph](https://developer.hashicorp.com/terraform/internals/graph) is a DAG, and its providers are separate processes that use a [gRPC plugin protocol](https://developer.hashicorp.com/terraform/plugin/terraform-plugin-protocol). kevin's plugin model uses the same idea.

The graphs have different purposes. Terraform's [state file](https://developer.hashicorp.com/terraform/language/state/purpose) maps configuration to long-lived resources, such as a virtual machine or a DNS record, across runs. kevin has no state file: it finds its resources by container labels, so a crashed run leaves nothing to reconcile. Terraform has no console, proxy, or egress control.

Use Terraform for the cloud resources an environment depends on. Use kevin for the local environment itself.

## Docker Compose

Compose is the closest in use: both start a set of containers from a file and remove them on command.

Compose orders containers with [`depends_on`](https://docs.docker.com/compose/how-tos/startup-order/). kevin runs every step with no dependency in parallel. By default, `depends_on` waits until a container is running, and Compose needs a health check to wait for readiness. A kevin container step is ready once the container is running, and a `wait` step can add a TCP, HTTP, `kubectl`, or command check.

Compose is for [a single host](https://docs.docker.com/compose/intro/features-uses/). It has no Kubernetes step, no TLS-terminating proxy, no egress control, and no way to send a real hostname to a local container without editing `/etc/hosts`.

For a few containers with no need for traffic control or Kubernetes, Compose is simpler. kevin fits when the environment includes Kubernetes, or when you need to see and control traffic between services.

## Tilt

[Tilt](https://tilt.dev) is for Kubernetes. Its main feature is [live update](https://docs.tilt.dev/tutorial/5-live-update.html): it syncs code into a running container without a rebuild, and shows the result in a [web UI](https://docs.tilt.dev/tutorial/3-tilt-ui.html). A Tiltfile is a [Starlark](https://docs.tilt.dev/tiltfile_concepts.html) program, not a declared graph of steps.

kevin does not watch your source code or sync it into containers. kevin starts and removes an environment of containers and Kubernetes clusters in dependency order, with a TLS-terminating, egress-controlled proxy in front.

For fast changes to code in a cluster, use Tilt. To start and remove a mixed environment the same way each time, with egress control, use kevin.

## Shell scripts

A script needs nothing new to install or learn. For a few containers that rarely fail partway, a script is enough.

As the script grows, you write by hand what kevin provides:

- Parallel steps need [`&` and `wait`](https://www.gnu.org/software/bash/manual/html_node/Job-Control-Builtins.html) job control in every script.
- Cleanup needs a [`trap`](https://www.gnu.org/software/bash/manual/html_node/Signals.html). If one exit path skips it, a container stays running.
- There is no view of the traffic between services, other than `docker logs`.
- There is no egress control without your own firewall rules.
- A new kind of service is more script, with no schema and no check of what one step gives the next.
