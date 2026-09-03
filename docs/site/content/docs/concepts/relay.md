---
title: "Relay"
description: "The in-network DNS/TLS forwarder: lifecycle, pod routing, the cluster tunnel, and its limits."
weight: 10
---

# Relay

The relay is a container on the shared docker network. It answers DNS queries for the environment domain with its own address. It forwards HTTP and TLS traffic to the proxy on the host.

The relay carries no routing table and no egress policy. The proxy remains the single place that holds both. A workload that resolves a step through the relay still reaches the proxy, and the proxy routes the request by the Host header, the same as it routes every other request.

## Lifecycle

The relay container's name is deterministic (`kevin-<project>-relay`), and `Start` reuses one already running rather than recreating it - a persisted `setup`-scope resource (a `kind` cluster's CoreDNS patch, say) that baked in the relay's address at `Up` time needs that address to survive the process that created it exiting. `kevin setup` leaves its relay running; `kevin run` afterward reuses it; `kevin teardown` is what finally removes it, once neither scope still needs it.

A reused container must still forward to a live proxy, and the proxy's gateway listener binds an OS-assigned port fresh each process by default - so `Start` compares the running container's recorded domain/proxy address against what this process would use, and replaces it on a mismatch rather than reusing a relay that would silently forward nowhere. The gateway listener tries the port a previous process in the same workspace recorded first, which keeps that address (and therefore the relay) stable across most process boundaries; the replace-on-mismatch path only fires when that port could not be reused, or the domain changed.

## Pod routing

The kind plugin patches the CoreDNS Corefile of the cluster after the nodes join the shared network. The patch adds a forward zone for the environment domain that points at the relay. The plugin restarts CoreDNS to load the change.

A pod then resolves `<step>.<domain>` through the cluster DNS. The pod needs no proxy environment variable of its own.

A pod reaches a step across two hops on the host. The pod resolves the name through CoreDNS and connects to the relay over the docker network. The relay forwards the connection to the proxy on the host. The proxy then connects to the published port of the step, on the host again.

## Gateway bind

The proxy binds a second listener on the gateway address of the shared network. A container reaches the proxy there directly. `proxy.gateway_port` in `kevin.cue` pins that listener's port instead of the default persist-and-reuse behavior described above.

Docker Desktop on macOS and on Windows runs the daemon inside a virtual machine. The gateway address exists only inside that machine. A bind from the host fails with `EADDRNOTAVAIL`. The relay then reaches the proxy through `host.docker.internal` instead.

Plain Linux Docker runs the daemon on the host. The gateway bind succeeds there, and the relay uses that listener.

## Cluster tunnel

The relay's other job runs the opposite direction: `kevin-relay` also has a `-socks5-listen` mode, mutually exclusive with `-domain`/`-proxy`, that runs a SOCKS5 server instead of the DNS/HTTP forwarder. A `builtin:kind` step's `with.expose` list stands up exactly one such relay as a Pod inside the cluster, so a client outside the cluster can dial an arbitrary in-cluster address (a Service DNS name or a Pod IP, with its port) by asking the relay to `CONNECT` to it.

This exists because a kind cluster's `extraPortMappings` are fixed at cluster creation, before `Up` has created anything, unlike a container step's port publish. One relay avoids needing a static port mapping per exposed service: `Up` picks one host port, bakes one `extraPortMappings` entry for it into the generated cluster config (pointed at the control-plane node), loads the `kevin-relay` image into that same node, and applies the relay Pod with `kubectl apply` run inside the node, the same `docker exec`-wrapped `kubectl` mechanism the CoreDNS patch already uses, pinned to that same control-plane node.

A `builtin:kind` step's `Up` does not wait for an `expose` entry's address to become dialable, unlike a container step's `expose` waiting on its own port. kind doesn't own what an entry names. The target is usually deployed separately, by a manifest applied after the cluster is up, so `Up` only wires the relay and reports the address. `Result.ExposedPorts` itself never reaches a dependent step's wire request, so the engine mirrors each entry's relay address into the `needs` variable's `system` sub-namespace, as `needs.<step>.system.expose_<name>`, kept separate from `out` (a step's own `Outputs`) specifically so a kevin-computed key can never collide with one a plugin chose for its own output (see [Cross-step values]({{< relref "/docs/environment-file#cross-step-values" >}})). This mirroring is generic engine behavior, not kind-specific; it runs for any plugin's `ExposedPorts`. A `builtin:wait` step's `tcp` check reads `needs.<step>.system.expose_<name>` to close the readiness gap generically, dialing through the relay the same way a [`kubectl` or `helm`]({{< relref "/docs/guides/deploying-workloads" >}}) step applies a manifest into the same cluster.

An `expose` entry reports through the same `Result.ExposedPorts` a container step's `expose` already populates. No protocol or console change was needed. A plain `host:port` isn't enough information here, since a client must dial the relay and then ask it to reach the real target, so `Upstream` carries both as one string: `socks5://127.0.0.1:<relayPort>/<address>`, and `Protocol` reads `"socks5"` rather than `"tcp"`/`"udp"`, marking that it needs a SOCKS5-aware dial rather than a direct one.

The engine doesn't make a client do that dial itself. For any step's `ExposedPort` with `Protocol == "socks5"` (not kind-specific, any plugin's), it opens one more loopback listener of its own and forwards every accepted connection through a SOCKS5 dial to the target. A plain client like `psql` just connects to that local port with no SOCKS5 awareness at all. The address is mirrored into `system` too, as `needs.<step>.system.forward_<name>`, alongside the raw `expose_<name>` entry, and shown in the console as its own row next to the `socks5://...` one.

## Subdomain routing

`builtin:route` is the one mechanism for putting a step on the environment domain: `container`, `kind`, or a third-party plugin, any of them, through the exact same `with.routes` list. An entry either names an address the proxy process can dial directly (a `container` step's published loopback address, read from its `Outputs`), or, when the step sets `relay`, a target reached by tunneling through a relay, typically a Kubernetes Service inside a `kind` cluster.

The relay-tunneled half reuses `expose`'s existing mechanism. `expose` reaches into a cluster for a raw TCP client. It never goes through the proxy, and it has no concept of a hostname. `builtin:route` reuses the exact same relay Pod and the same `Upstream` convention `expose` already established, a `socks5://` URL whose path carries the real target, but for `Route` instead of `ExposedPort`: it needs nothing new from the wire protocol or the plugin SDK, only a small addition to the proxy itself, since a route's `Upstream` must be something *the proxy process* can dial, and a relay-tunneled target plainly isn't.

The proxy's own outbound dial logic reads one piece of request-scoped context: when a request matches a `Route` whose `Upstream` parses as that `socks5://` shape, it rewrites the dial target to the relay's own address (a real, loopback-published address, dialable exactly like any other route's upstream) and stashes the real target alongside it. The dial itself then connects to the relay and issues a SOCKS5 `CONNECT` for that target, instead of treating the relay's own address as the destination. This is the same handshake the engine already performs for a `socks5`-protocol `ExposedPort`, just one layer further down, inside the proxy's own outbound dial rather than an engine-managed local listener. A WebSocket upgrade goes through the identical dial path, so it inherits the same relay-awareness for free.

`builtin:route` itself does no Kubernetes work at all. It isn't `kind`-specific, only convention-compatible with it. It takes a relay address and a list of host/address pairs, and returns one `Route` per pair with `Upstream` built in that same `socks5://` shape. Because `kind`'s relay Pod deploys only when `expose` is non-empty, `kind` gained a `relay: bool` field to opt in to the Pod with no `expose` entries, and publishes the relay's address as a `relay_addr` output for a downstream `route` step to read, keeping `kind` itself decoupled from whatever `kubectl`/`helm` step actually deploys the routed service. DAG ordering (`route`'s `needs` naming both the cluster and the deploying step) is what makes the target address meaningful by the time `route`'s `Up` runs.

## Limits

The relay resolves a name. It does not control egress. A workload that resolves an external name through the relay gets the real address of that name, and connects to the internet directly.

Egress control still needs the proxy environment variables on a workload. A step still needs the allow list to reach an external host.

Published ports remain necessary. The host proxy reaches a workload through the loopback address that the container plugin publishes. The proxy runs on the host and cannot resolve a network alias.
