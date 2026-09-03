---
title: "Kubernetes clusters"
description: "Bring up a local Kubernetes cluster with kind, on the same docker network as everything else."
weight: 3
---

# Kubernetes clusters

A `builtin:kind` step brings up a local Kubernetes cluster with [kind](https://kind.sigs.k8s.io/). Its nodes join kevin's shared docker network as well as kind's own, so a container step and a pod reach each other by name. It also publishes a kubeconfig path a tool on the host uses directly:

```sh
kevin -C examples/kind run
KUBECONFIG=.kevin/kubeconfig/kind-example-cluster kubectl get nodes
```

Or declare a `commands:` entry that reads the path for you and execs `kubectl` with it, so you don't have to retype `--kubeconfig` every time:

```cue
commands: nodes: {
    needs: ["cluster"]
    run: ["kubectl", "--kubeconfig", "${needs.cluster.out.kubeconfig}", "get", "nodes"]
}
```

```sh
kevin -C examples/kind do nodes
```

`Up` recreates the cluster if one with the same name already exists (e.g. left over from a crash), so re-running `kevin run` is safe.

## Registry pulls

A pull that a pod triggers goes through kevin's proxy like any other request from a node, and the proxy presents a leaf signed by the kevin CA. kind installs the kevin root certificate into every node, so that verifies with no extra work. Set `trust_ca: false` in the step's `with` block to turn the install off.

## Workloads

kind only brings up the cluster. See [Deploying workloads]({{< relref "deploying-workloads" >}}) for how to actually put a workload inside one.

## Service routing

`expose` reaches an arbitrary in-cluster address as raw TCP. It never goes through the proxy, and has no notion of a hostname. To instead serve a Service in a browser under a subdomain, set `relay: true` on the `kind` step (standing up the relay pod even with no `expose` entries) and add a [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) step reading its `relay_addr` output. See [Name resolution]({{< relref "relay-and-name-resolution" >}}) for the full picture, including why `route` works identically for a `builtin:container` step's published port.

See [Reference]({{< relref "/docs/reference/steps/kind" >}}) for the full `with` block, including `egress` (external hosts the cluster's nodes can reach), `expose` (reaching an arbitrary in-cluster address from the host), and `relay`.
