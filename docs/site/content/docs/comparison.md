---
title: "⚖️ kevin vs. other tools"
weight: 3
---

# kevin vs. other tools

kevin overlaps with a handful of tools people already reach for. None of them do the same things at once: an ephemeral local environment, a real parallel DAG, a TLS-terminating egress-controlled proxy that can intercept a real-world hostname and redirect it to a local container, and a language-agnostic plugin protocol. This page goes through each tool in turn, from the closest in spirit to the furthest, with what's actually alike and what's actually different, not just the one-line table version.

## Garden

[Garden](https://garden.io) is the closest architectural relative. A Garden project is a [DAG of actions](https://docs.garden.io/reference/glossary#action-graph) with dependencies between them, executed by a [provider/plugin model](https://docs.garden.io/reference/providers), conceptually the same shape as kevin's step DAG and provider-per-plugin-process model, and it's meant for the same job: spinning up dev/test environments, locally or in CI.

Where it diverges is scope and weight. Garden's [project configuration](https://docs.garden.io/using-garden/configuration-overview) spans a project config, one or more action types (`Build`, `Deploy`, `Run`, `Test`), providers, and workflows: a bigger surface to learn than kevin's single `kevin.cue` with a flat `env:` map. Garden also has no equivalent to kevin's proxy: no TLS termination on traffic between services under test, no egress allow-list, no per-request log of what crossed the wire, and no way to intercept a real third-party hostname and redirect it to a local fake. If you're already deep in Garden's action-graph model and don't need traffic visibility or egress control, there's little reason to switch. If what you want is "bring up a small mixed Docker+Kubernetes environment and watch/control what it talks to," kevin is the narrower, purpose-built tool.

## Terraform (or OpenTofu)

Terraform's [dependency graph](https://developer.hashicorp.com/terraform/internals/graph) is a real DAG. Like kevin, its providers are separate processes speaking a [gRPC plugin protocol](https://developer.hashicorp.com/terraform/plugin/terraform-plugin-protocol) over `go-plugin`. Kevin's provider model is the same idea, but applied to local dev instead of cloud infrastructure.

The difference is what the graph is *for*. Terraform's [state file](https://developer.hashicorp.com/terraform/language/state/purpose) exists to map configuration to durable, long-lived real-world resources (an EC2 instance, a DNS record), tracked across runs indefinitely. kevin has no state file at all: every `Up`/`Down` derives from live Docker state (containers carry `kevin.project`/`kevin.step` labels), so a crashed run leaves nothing to reconcile and a fresh `kevin run` just works. Terraform also has no console, no proxy, and no egress control: it provisions infrastructure, it doesn't sit in front of the traffic between running services. Use Terraform for the cloud resources an environment depends on. Use kevin to provision local, ephemeral dev environments.

## Docker Compose

Compose is the most common thing people reach for first, and the most directly comparable at the surface: both bring up a set of Docker containers from a declarative file and tear them down on command. But Compose's ordering is only [`depends_on`](https://docs.docker.com/compose/how-tos/startup-order/): "start this after that's running," not a scheduler that fans out everything with no dependency between them in parallel the way kevin's DAG does. And per Compose's own docs, `depends_on` waits for a container to be *running*, not *ready*: a `container` step in kevin is ready once its published port accepts a connection, and a `wait` step can chain a richer check (HTTP status, `kubectl rollout status`, an exec probe) onto any step that needs one.

Compose is also explicitly [scoped to single-host deployments](https://docs.docker.com/compose/intro/features-uses/): no Kubernetes step, no built-in TLS-terminating proxy, no egress control, and no way to point a real hostname like `s3.amazonaws.com` at a local container without editing `/etc/hosts` yourself. If your environment is "a few containers on my machine" and you don't need traffic visibility, egress control, or a Kubernetes cluster in the mix, Compose's simplicity is hard to beat. kevin exists for the point where the environment grows past that: mixed Docker+Kubernetes, or you need to see and control what's crossing between services.

## Tilt

[Tilt](https://tilt.dev) is [built for Kubernetes](https://tilt.dev) specifically. Its core loop is [live-updating](https://docs.tilt.dev/tutorial/5-live-update.html) a container already running in a cluster in place, skipping a full rebuild-and-redeploy, surfaced through a real-time [web UI](https://docs.tilt.dev/tutorial/3-tilt-ui.html). A Tiltfile is [Starlark](https://docs.tilt.dev/tiltfile_concepts.html) (a Python dialect), which makes it a real program rather than a declared graph: useful for conditionals and loops, but Tilt doesn't model bring-up as a dependency DAG the way kevin or Terraform do; it's closer to "run this script, then watch, and re-sync."

That live-update loop is Tilt's core to its value proposition, and kevin doesn't try to compete with it: kevin doesn't watch your source tree or push code into a running container. What kevin has that Tilt doesn't: DAG-based bring-up/teardown of a *mixed* Docker+Kubernetes environment (not just Kubernetes), and a TLS-terminating, egress-controlled proxy in front of it. If you're already iterating on workloads deployed to a cluster, Tilt is the better fit for that inner loop. If you need to stand the whole mixed environment up and down repeatably with controlled egress, that's kevin's job, not Tilt's.

## Shell scripts

This is where we often start, and honestly, it may be all you need: full control, zero setup, nothing new to install or learn. For a couple of containers that rarely fail partway through, a script is still the right call.

Where it stops scaling is what a DAG engine and a plugin protocol exist to fix: 

- Bash parallelism means hand-rolling [`&`/`wait`](https://www.gnu.org/software/bash/manual/html_node/Job-Control-Builtins.html) job control yourself, in every script, correctly, every time. 
- Cleanup means a [`trap`](https://www.gnu.org/software/bash/manual/html_node/Signals.html) you write and maintain by hand, miss one exit path and a container survives the script that made it. 
- There's no visibility into the traffic between services beyond whatever you remember to pipe into `docker logs`
- No egress control at all: nothing stops a step from reaching the internet unless you build a firewall rule for it yourself. 
- Adding a new kind of service is just more bash: no plugin boundary, no schema, no enforced contract between what one step promises and what the next one expects. 

kevin is what you reach for once the script has grown enough steps, enough failure modes, or enough services talking to each other that hand-rolling all of that yourself stops being the fast option.
