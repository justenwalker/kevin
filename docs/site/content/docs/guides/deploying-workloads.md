---
title: "📦 Deploying workloads"
weight: 4
---

# Deploying workloads

A `builtin:kind` step only brings up a cluster; it doesn't put anything inside one. The `kubectl` and `helm` steps close that gap. Each shells out to the real `kubectl`/`helm` binary on your host, against a `needs` edge on a step that publishes a `kubeconfig` and a `context`, typically a kind step:

```cue
env: {
    cluster: uses: "builtin:kind"
    app: {
        uses: "builtin:kubectl"
        needs: ["cluster"]
        with: {
            kubeconfig: "${needs.cluster.out.kubeconfig}"
            context:    "${needs.cluster.out.context}"
            manifest:   "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: demo\n"
        }
    }
}
```

Both steps read `kubeconfig`/`context` off `needs` with a `${...}` expression. See [Environment file: cross-step values]({{< relref "/docs/configuring-an-environment#cross-step-values" >}}).

## kubectl

Set exactly one of `manifest` (inline YAML), `path` (a manifest file or directory), or `kustomize` (a directory, applied with `-k`). `Up` rejects a `with` block that sets zero or more than one. See [kubectl reference]({{< relref "/docs/reference/kubectl" >}}) for the full `with` block.

## Helm

Name a `chart` (a local path, an `oci://` reference, or a chart name inside `repo`) and a `release`. `post_renderer`/`post_renderer_args` plumb straight through to `helm upgrade --install --post-renderer`. kevin doesn't implement rendering itself; it only forwards the flag. See [helm reference]({{< relref "/docs/reference/helm" >}}) for the full `with` block.

`path`, `kustomize`, `chart`, and `values_files` all resolve against the project directory (the directory holding `kevin.cue`) when given as a relative path.

## No teardown

kevin didn't create the cluster these steps target, and doesn't tear it down. Deleting a namespace or uninstalling a release would reach into state outside what kevin owns. What `kubectl apply` or `helm upgrade --install` leaves behind survives `kevin teardown` and Ctrl-C.

## Readiness

`helm upgrade --install` waits for its own release by default (`wait: "5m"` in its `with` block), but `kubectl apply` doesn't wait for anything, and neither step's `Down` matters for gating a *dependent* step on the workload actually being up. `builtin:wait` closes that gap: add a step with a `needs` edge on the one that applied the manifest, and a check that only succeeds once the workload is ready.

```cue
app_ready: {
    uses: "builtin:wait"
    needs: ["cluster", "app"]
    with: {
        timeout: "2m"
        kubectl: {
            kubeconfig: "${needs.cluster.out.kubeconfig}"
            context:    "${needs.cluster.out.context}"
            resource:   "deployment/app"
            rollout:    true
        }
    }
}
```

A `kubectl` check runs `kubectl wait --for=<condition>` or `kubectl rollout status`, retrying while the resource doesn't exist yet. There is no need to sequence it after the apply beyond the `needs` edge. `builtin:wait` also has `tcp`, `http`, and `exec` checks, for a step whose readiness isn't a kubectl condition. A `tcp` check reaches a service inside a kind cluster through the SOCKS5 relay, dialing the `needs.<step>.system.expose_<name>` value a `builtin:kind` step's `expose` entries publish (see [Cluster tunnel]({{< relref "/docs/concepts/architecture#cluster-tunnel" >}})). See [wait reference]({{< relref "/docs/reference/wait" >}}) for every check kind, and [`examples/kind`](https://github.com/justenwalker/kevin/tree/main/examples/kind) for a full chain: a `kubectl` step and a `helm` step each gated by their own `wait` step, plus a `tcp` check through the relay and an `http` check against a plain container.
