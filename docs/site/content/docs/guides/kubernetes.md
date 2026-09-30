---
title: "Kubernetes clusters"
description: "Add a local Kubernetes cluster to an environment with the kind or k3d driver."
weight: 3
---

# Kubernetes clusters

This guide adds a local Kubernetes cluster to an environment with [`builtin:kubernetes`]({{< relref "/docs/reference/steps/kubernetes" >}}).

## Prerequisites

- `kubectl` on your `PATH`.
- The command of your driver on your `PATH`: `kind` for the `kind` driver, `k3d` for the `k3d` driver.

## Add a cluster

```cue
env: cluster: {
    uses: "builtin:kubernetes"
    with: {
        driver: "kind"
        egress: ["docker.io", "*.docker.io", "*.docker.com"]
    }
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
    uses: "builtin:kubernetes"
    with: {
        driver:  "kind"
        workers: {worker_a: {}, worker_b: {}}
    }
}
```

## Mount a host directory into the nodes

List the directory in `mounts`. It appears in every node, including the workers:

```cue
cluster: {
    uses: "builtin:kubernetes"
    with: {
        driver:  "kind"
        workers: worker_a: {}
        mounts: [{host: "src", container: "/workspace"}]
    }
}
```

A relative `host` path is relative to the project directory. Add `readonly: true` to mount it read-only. See the [`builtin:kubernetes` reference]({{< relref "/docs/reference/steps/kubernetes" >}}#mount) for the fields.

## Deploy workloads

See [Deploying workloads]({{< relref "deploying-workloads" >}}).

## Reach a Service from the host

Add an `expose` entry for the Service address:

```cue
cluster: {
    uses: "builtin:kubernetes"
    with: {
        driver: "kind"
        expose: db: address: "postgres.default.svc.cluster.local:5432"
    }
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
cluster: {uses: "builtin:kubernetes", with: {driver: "kind", relay: true}}
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

With `--engine podman`, the `kind` and `k3d` drivers run the nodes on Podman. See [Container engine]({{< relref "/docs/concepts/container-engine" >}}) for the limits.

## Use k3d instead

Set `driver` to `"k3d"`:

```cue
proxy: egress: {
    deny: true
    allow: ["docker.io", "*.docker.io", "*.docker.com"]
}

env: cluster: {
    uses: "builtin:kubernetes"
    with: {
        driver: "k3d"
        workers: worker_a: {}
    }
}
```

List the registries in `proxy.egress.allow`, not in the step's `egress`, so the k3s images can pull. Run `kevin run`: the `cluster` step reports `ready` once the nodes are up.

`kubeconfig`, `expose`, `relay`, `workers`, and `builtin:route` work as they do with `kind`. The [`builtin:kubernetes` reference]({{< relref "/docs/reference/steps/kubernetes" >}}) lists the limits of the `k3d` driver.

## Turn off k3s components

Name the bundled components to skip in `k3d.disable`:

```cue
env: cluster: {
    uses: "builtin:kubernetes"
    with: {
        driver: "k3d"
        k3d: disable: ["traefik", "metrics-server"]
    }
}
```

Run `kubectl --kubeconfig <kubeconfig> get pods -A`, with the `kubeconfig` output of the `cluster` step. The list no longer shows `traefik`. The [`builtin:kubernetes` reference]({{< relref "/docs/reference/steps/kubernetes" >}}#k3d) lists the component names.

## Related

- [`builtin:kubernetes` reference]({{< relref "/docs/reference/steps/kubernetes" >}})
- [Relay]({{< relref "/docs/concepts/relay" >}}): how pods reach the environment, and how the host reaches pods.
