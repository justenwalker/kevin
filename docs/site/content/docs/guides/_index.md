---
title: "🔧 Guides"
weight: 10
bookCollapseSection: true
---

# Guides

How to use each part of kevin: the proxy and egress control, the CA and trust store, Kubernetes clusters, deploying workloads into one, and name resolution through the relay.

## Example environments

Four example environments in the repository double as worked examples for these guides:

| Example | Shows |
|---|---|
| [`examples/web`](https://github.com/justenwalker/kevin/tree/main/examples/web) | A container step reached through the proxy: the [Quickstart]({{< relref "/docs/quickstart" >}}) path. |
| [`examples/echo`](https://github.com/justenwalker/kevin/tree/main/examples/echo) | A provider that creates no real resource, so it isolates the DAG itself: `a` runs first, `b` and `c` run in parallel once `a` is up, `d` waits for both, and a `boom` step fails on purpose to show teardown running in reverse through what came up. |
| [`examples/kind`](https://github.com/justenwalker/kevin/tree/main/examples/kind) | A Kubernetes cluster, a registry, and workloads deployed into it: see [Kubernetes clusters]({{< relref "kubernetes" >}}) and [Deploying workloads]({{< relref "deploying-workloads" >}}). |
| [`examples/trust`](https://github.com/justenwalker/kevin/tree/main/examples/trust) | Installing and removing the kevin CA from the machine's trust stores: see [CA and trust store]({{< relref "ca-and-trust" >}}). |
