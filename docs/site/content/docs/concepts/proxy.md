---
title: "Proxy"
description: "TLS termination, routing, and egress control, and why the proxy has to run on the host process."
weight: 5
---

# Proxy

kevin changes no file on the host. There is no entry in `/etc/hosts`, no file in `/etc/resolver`, and no DNS server for the host.

A client reaches the environment through the proxy. The client sets `HTTP_PROXY` and `HTTPS_PROXY`, or loads `http://<proxy>/proxy.pac`. The `CONNECT` handler of the proxy resolves a hostname against the internal registry of the engine. A step fills the registry when `Up` returns a `Route`.

The environment has a base domain, `kevin.home` by default. A `route` step puts a name on it. See [Subdomain routing]({{< relref "/docs/concepts/relay#subdomain-routing" >}}) for the general mechanism, which works the same whether the address behind the name is a container's published port or something behind a relay. A bare step name is not a route, because a name without a dot could shadow a real host.

The proxy serves a proxy auto-config file at `/proxy.pac`. The file matches the base domain on a suffix, thus a step added later needs no reload. It sends everything else direct, so normal browsing is untouched. The file names the proxy by the host that the browser asked for, which keeps a loopback address and a LAN address both working.

`NO_PROXY` lists the step names, so a workload that honors it reaches another workload straight over the docker network. Not every client honors it. Busybox `wget` ignores `NO_PROXY`, thus a step must also be reachable through the proxy under its full name.

One listener serves three roles.

1. A forward proxy that terminates TLS. The proxy mints a leaf certificate for the requested host, and signs the certificate with the kevin CA.
2. A reverse proxy. The proxy matches the Host header against the routing table and forwards to the workload.
3. An egress control. The proxy denies a host that no route covers and that no allow list covers.

Egress denies by default. `proxy: egress: deny: false` in `kevin.cue` disables the control. Every request then reaches the internet, as before milestone 7.

An allow entry is an exact host, such as `api.github.com`, or a leading-dot wildcard, such as `*.github.com`. A wildcard matches a subdomain. It does not match the bare domain: `*.github.com` matches `api.github.com`, not `github.com`. List the bare domain too when both must reach the internet. Matching ignores case and ignores any port. `proxy: egress: allow` in `kevin.cue` names hosts for the whole environment. A step names hosts for itself alone, through the `egress_allow` field of its `Up` result. A route that a step registers always reaches the proxy. A workload of the environment is not egress.

A denied request still completes TLS. The CONNECT handler MITMs every host, denied or not. The proxy answers a denied request with `403 Forbidden` instead of closing the connection. The page names the denied host. It shows the exact CUE to add, for the whole environment and for one step. The response carries `Cache-Control: no-store`, `Pragma: no-cache`, and `Expires: 0`. A browser must not serve a cached denial after the user fixes the allow list.

Containers on the docker network reach each other by container name through the embedded DNS of Docker. That path needs no change on the host.

The proxy owns `CONNECT`, the MITM, and the leaf signing itself - no third-party proxy library. A `CONNECT` hijacks the underlying connection, writes `200 Connection Established`, then hand-shakes TLS with a leaf the proxy mints on the fly (signed by the project's intermediate authority) and offers both `h2` and `http/1.1` over ALPN. The negotiated protocol decides how the decrypted traffic is served: `h2` gets a real HTTP/2 server; `http/1.1` (or no ALPN at all) is handed to a plain HTTP/1.1 server over a one-connection listener, so keep-alive and chunked encoding stay standard rather than hand-rolled. Either way the decrypted request reaches the same routing/egress-deny logic a plain forward-proxy request already goes through. A minted leaf carries whatever Subject kevin chooses, no fixed organization string imposed by a library.

A WebSocket upgrade gets its own explicit path, not a free ride from a generic reverse-proxy helper: the handshake is an ordinary HTTP request, so Host routing and the egress-deny check both apply to it exactly as they would to any other request, and it gets one entry in the traffic log for the handshake itself. Only the frame stream after a successful upgrade is an unrouted, unlogged raw pipe (kevin hijacks the connection and copies bytes bidirectionally once it sees a `101` with `Upgrade` in the response). This is the same shape of gap `expose` already exists to name honestly for non-HTTP traffic in general, rather than something WebSocket-specific.

## Host-bound

The proxy runs in the engine process, not in a container. It therefore cannot resolve a network alias, and on macOS it cannot reach a container address either. A route must name an address that the host can reach.

The container plugin publishes the port of a step on the loopback when the step declares a `host`, and returns that published address as the upstream. A step reaches another step by step name, and the proxy reaches a step by its published port.

A step that serves a name is ready when its published port accepts a connection, not when the container starts. A started container reports `Running` before the process inside binds its port.
