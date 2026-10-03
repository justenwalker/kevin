---
title: "Proxy"
description: "TLS termination, routing, and egress control, and why the proxy runs on the host."
weight: 5
---

# Proxy

kevin changes no file on your machine: no entry in `/etc/hosts`, no file in `/etc/resolver`, and no DNS server. Clients on the host reach the environment through the proxy instead, with `HTTP_PROXY` and `HTTPS_PROXY`, or with the auto-config file at `/proxy.pac`.

## Names

The environment has a base domain, `kevin.home` by default. A [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) step adds names under it. When a step's `Up` returns routes, kevin adds them to the proxy's routing table.

A bare step name is never a route. A name with no dot could hide a real host.

The auto-config file matches the base domain by suffix, so a route added later needs no reload. It sends all other traffic directly. It names the proxy by the host that the browser used to fetch it, so it works from a loopback address and from a LAN address.

## One listener, three jobs

1. **Forward proxy with TLS termination.** For a `CONNECT`, the proxy signs a certificate for the requested host with the project CA, and completes the TLS handshake itself.
2. **Reverse proxy.** The proxy matches the `Host` header against the routing table and forwards the request.
3. **Egress control.** The proxy blocks a host that no route and no allow list covers.

The proxy implements `CONNECT` and TLS termination itself, with no third-party proxy library. After a `CONNECT`, it takes over the connection, replies `200 Connection Established`, and completes a TLS handshake that offers `h2` and `http/1.1`. An `h2` connection goes to an HTTP/2 server. Any other connection goes to a standard HTTP/1.1 server over a one-connection listener. Both paths then use the same routing and egress checks as a plain proxy request.

A WebSocket upgrade is an HTTP request, so routing and egress checks apply to it, and the console logs it. After the `101` response, the proxy copies bytes in both directions and does not log the frames.

## Routes that skip TLS termination

A route with `mode: "passthrough"` forwards the client's TLS connection to the upstream unchanged. The client checks the upstream's own certificate, such as one from cert-manager in a kind cluster. The console logs one entry for the connection. This mode requires `tls: true`: an upstream without TLS has no certificate to pass through.

A route with `mode: "raw"` does the same for a protocol that is neither TLS nor HTTP, such as a database protocol. It requires `tls: false`.

## Egress control

`proxy.egress.deny` has no default: the environment file must set it. A field with a default cannot take a value from a `@tag` without an extra `if` block, so leaving out the default lets `deny: bool @tag(...)` work directly. See [Per-machine and per-run settings]({{< relref "/docs/guides/local-and-per-run-settings" >}}).

An allow entry is an exact host, such as `api.github.com`, or a wildcard, such as `*.github.com`. A wildcard matches subdomains, not the bare domain. Matching ignores case and port. `proxy.egress.allow` applies to every step. A step can add hosts for itself through the `egress_allow` field of its `Up` result. A routed name is part of the environment, so egress control never blocks it.

By default, the proxy terminates TLS for every host with no route, allowed or not. It then answers a blocked request with a `403 Forbidden` page, not a closed connection. The page names the host and shows the CUE that allows it. The response has `Cache-Control: no-store`, `Pragma: no-cache`, and `Expires: 0`, so a browser does not show a cached denial after you change the allow list.

With `proxy.egress.passthrough: true`, the proxy checks the host in the `CONNECT` request before any TLS. It forwards an allowed host's connection unchanged, and answers a blocked host with `403` in the `CONNECT` response. A client that does not trust the kevin CA can then reach allowed hosts. Routes are not affected.

## Which traffic reaches the proxy

The relay sends all TCP traffic of a `builtin:container` step on ports 80 and 443 to the proxy, whatever the container resolved and whatever proxy variables it has. See [Relay]({{< relref "/docs/concepts/relay#traffic-capture" >}}). Egress control therefore applies to every container.

A `builtin:exec` step runs on the host, and relies on the proxy variables. For a `builtin:kubernetes` pod, the relay captures traffic at the node.

`NO_PROXY` lists the step names, so a client that honors it reaches another step directly over the project network. Some clients ignore `NO_PROXY`, such as busybox `wget`, so each step is also reachable through the proxy by its full name.

## Why the proxy runs on the host

The proxy runs in the `kevin` process, not in a container. It cannot resolve a network alias, and on macOS it cannot reach a container address. So a route must name an address that the host can reach.

`builtin:container` publishes an `expose` port on the host loopback address, and reports that address as an output. Steps reach each other by step name. The proxy reaches a step by its published port.

A container reports `Running` before the process inside it listens on its port. A container step is ready once the container is running, so a route can reach it before the process listens. Add a `wait` step to hold dependents until the service answers.
