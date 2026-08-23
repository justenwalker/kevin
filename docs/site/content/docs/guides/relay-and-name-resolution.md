---
title: "📡 Name resolution"
weight: 5
---

# Name resolution

A workload resolves `<step>.<domain>` with zero configuration of its own: no proxy environment variable, no DNS setup. An in-network relay container answers DNS for the environment domain and forwards the connection to the proxy on the host. Both a container step and a pod inside a kind cluster reach a step this way.

kevin runs `ghcr.io/justenwalker/kevin/relay:<version>` by default (`kevin-relay:dev`, built locally with `./build/gnob relay-image`, for an unreleased build). `KEVIN_RELAY_IMAGE` overrides the image outright; set `relay: image:` in `kevin.cue` to pin it there instead (the env var still wins if both are set). `KEVIN_RELAY_REPO` and `KEVIN_RELAY_TAG` each override just the repository or just the tag of whichever image would otherwise apply, for mirroring the image to a private registry without also tracking its version tag. Set `relay: enabled: false` in `kevin.cue` to turn the relay off. A workload then needs the proxy environment variables to reach a step.

A pod resolves a step through the cluster's own DNS (kevin patches CoreDNS automatically when a kind step comes up) and reaches the relay over the shared docker network, which forwards to the proxy on the host. No proxy configuration inside the pod is needed either.

## Kind clusters

`with.expose` on a `builtin:kind` step lets something outside the cluster dial an arbitrary in-cluster address (a Service DNS name or a Pod IP, with its port). This is useful for reaching something deployed into the cluster that isn't fronted by an HTTP(S) route:

```cue
cluster: {
    uses: "builtin:kind"
    with: expose: [{address: "postgres.default.svc.cluster.local:5432", name: "db"}]
}
```

Unlike a container step's `expose`, `Up` doesn't wait for the address to become dialable. The target is usually deployed separately, after the cluster is up, so `Up` only wires the tunnel and reports where to reach it. See [kind reference]({{< relref "/docs/reference/kind" >}}) for the `expose` field.

## HTTP routing

`expose` is a raw TCP tunnel, dialed directly by a tool that speaks SOCKS5 or by an engine-managed local forward. It never goes through the proxy, and it has no notion of a hostname. To instead serve `myapp` as a subdomain of the environment domain in a browser, backed by a Service inside a kind cluster, add a [`builtin:route`]({{< relref "/docs/reference/route" >}}) step. It reuses the same relay: set `relay: true` on the `kind` step to stand the relay pod up even with no `expose` entries, read its address back as `relay_addr`, and register a route through it:

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

A brand-new route needs no DNS setup either. The proxy's `proxy.pac` already sends the whole environment domain through the proxy by string suffix, so it's reachable from a PAC-configured browser the moment the step registers it, the same as any other route.

## Limits

The relay resolves names; it does not control egress. A workload that resolves an external name through the relay reaches the internet directly. Egress control still needs the proxy environment variables on that workload. See [Proxy and egress]({{< relref "proxy-and-egress" >}}).
