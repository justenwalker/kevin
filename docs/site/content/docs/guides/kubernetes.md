---
title: "Kubernetes clusters"
description: "Add a local Kubernetes cluster to an environment with kind."
weight: 3
---

# Kubernetes clusters

This guide adds a local Kubernetes cluster to an environment with [`builtin:kind`]({{< relref "/docs/reference/steps/kind" >}}).

## Prerequisites

- `kind` and `kubectl` on your `PATH`.

## Add a cluster

```cue
env: cluster: {
    uses: "builtin:kind"
    with: egress: ["docker.io", "*.docker.io", "*.docker.com"]
}
```

`egress` lists the registries that the nodes pull images from when `proxy.egress.deny` is `true`. Pods and containers in the environment reach each other by name.

To keep the cluster between runs, put the step in `setup` instead of `env` and start it with `kevin setup`.

## Use `kubectl` from the host

The step's `kubeconfig` output is the path of its kubeconfig file. For the example in the repository:

```sh
kevin -C examples/kind run
KUBECONFIG=examples/kind/.kevin/kubeconfig/kind-example-cluster kubectl get nodes
```

To avoid typing the path, add a command:

```cue
commands: nodes: {
    needs: ["cluster"]
    run: ["kubectl", "--kubeconfig", "${needs.cluster.out.kubeconfig}", "get", "nodes"]
}
```

```sh
kevin -C examples/kind do nodes
```

## Add worker nodes

```cue
cluster: {
    uses: "builtin:kind"
    with: workers: {worker_a: {}, worker_b: {}}
}
```

## Deploy workloads

See [Deploying workloads]({{< relref "deploying-workloads" >}}).

## Reach a Service from the host

Add an `expose` entry for the Service address:

```cue
cluster: {
    uses: "builtin:kind"
    with: expose: db: address: "postgres.default.svc.cluster.local:5432"
}
```

The step's `forward_db` system value is a `127.0.0.1:<port>` address. The console shows it. Any TCP client can connect to it:

```sh
psql -h 127.0.0.1 -p <port> -U postgres
```

The step does not wait for the Service to exist. To wait, add a [`builtin:wait`]({{< relref "/docs/reference/steps/wait" >}}) step with `tcp: address: "${needs.cluster.system.expose_db}"`.

## Give a Service a name on the environment domain

Set `relay: true` on the cluster, and add a [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) step:

```cue
cluster: {uses: "builtin:kind", with: {relay: true}}
app:     {uses: "builtin:kubectl", needs: ["cluster"], with: {...}}

app_route: {
    uses:  "builtin:route"
    needs: ["cluster", "app"]
    with: {
        relay: "${needs.cluster.out.relay_addr}"
        routes: [{host: "myapp", address: "myapp.default.svc.cluster.local:80"}]
    }
}
```

The Service is now `myapp.kevin.home` through the proxy. Pods can also resolve it by that name.

## Use Podman

With `--engine podman`, kind runs the nodes on Podman. kind's Podman support is experimental.

## Related

- [`builtin:kind` reference]({{< relref "/docs/reference/steps/kind" >}})
- [Relay]({{< relref "/docs/concepts/relay" >}}): how pods reach the environment, and how the host reaches pods.
