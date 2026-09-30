---
title: "Deploying workloads"
description: "Deploy manifests and Helm charts into a cluster, and wait until they are ready."
weight: 4
---

# Deploying workloads

This guide deploys into a cluster from a [`builtin:kubernetes`]({{< relref "/docs/reference/steps/kubernetes" >}}) step. See [Kubernetes clusters]({{< relref "kubernetes" >}}) to add one.

## Prerequisites

- `kubectl`, and `helm` for Helm charts, on your `PATH`.

## Apply manifests

Add a [`builtin:kubectl`]({{< relref "/docs/reference/steps/kubectl" >}}) step that reads the cluster's kubeconfig:

```cue
app: {
    uses:  "builtin:kubectl"
    needs: ["cluster"]
    with: {
        kubeconfig: "${needs.cluster.out.kubeconfig}"
        context:    "${needs.cluster.out.context}"
        path:       "k8s/app.yaml"
    }
}
```

Set one of:

- `manifest`: YAML in the environment file.
- `path`: a manifest file or directory.
- `kustomize`: a kustomization directory.

A relative path resolves against the project directory.

## Install a Helm chart

Add a [`builtin:helm`]({{< relref "/docs/reference/steps/helm" >}}) step:

```cue
db: {
    uses:  "builtin:helm"
    needs: ["cluster"]
    with: {
        kubeconfig: "${needs.cluster.out.kubeconfig}"
        context:    "${needs.cluster.out.context}"
        release:    "db"
        chart:      "oci://registry-1.docker.io/bitnamicharts/postgresql"
    }
}
```

`chart` is a local path, an `oci://` reference, or a chart name in `repo`. By default, the step waits up to 5 minutes for the release to become ready.

## Wait until a workload is ready

`kubectl apply` does not wait. Add a [`builtin:wait`]({{< relref "/docs/reference/steps/wait" >}}) step, and put it in the `needs` of the steps that use the workload:

```cue
app_ready: {
    uses:  "builtin:wait"
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

The check retries until the resource exists and is ready. `builtin:wait` also has `tcp`, `http`, and `exec` checks.

## Keep resources after teardown

By default, teardown deletes what `kubectl` applied and uninstalls what `helm` installed. To keep them, set `keep: true` in the step's `with` block.

## Related

- [`examples/kind`](https://github.com/justenwalker/kevin/tree/main/examples/kind): a `kubectl` step and a `helm` step, each with a `wait` step.
