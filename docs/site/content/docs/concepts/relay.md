---
title: "Relay"
description: "The relay container: name resolution, traffic capture, tunnels into the network, fault injection, and its limits."
weight: 10
---

# Relay

The relay is a container on the project network. It has four jobs:

1. Answer DNS for the environment domain, so workloads can resolve route names.
2. Send the traffic of containers to the proxy on the host.
3. Let the host reach ports inside the project network or a kind cluster.
4. Apply network faults for [`builtin:fault`]({{< relref "/docs/reference/steps/fault" >}}).

The relay has no routing table. The proxy is the only place that routes. The relay makes sure traffic reaches the proxy.

## Lifecycle

The relay container has a fixed name, `kevin-<project>-relay`. If it is already running, kevin uses it. A `setup` step can depend on the relay's address, such as the DNS forward that a kind cluster keeps. That address must stay the same after the `kevin setup` process exits. `kevin setup` leaves the relay running, `kevin run` uses it, and `kevin teardown` removes it when neither scope needs it.

kevin compares the domain and proxy address of a running relay with its own. If they differ, it replaces the relay, which would otherwise forward to an address where no proxy listens. This happens only when `domain` or `proxy.gateway_port` changes.

## Reaching the proxy

The proxy has a second listener on the gateway address of the project network, at `proxy.gateway_port`. Containers reach the proxy there.

Docker Desktop on macOS and Windows runs the daemon in a virtual machine, and the gateway address exists only there. Binding it from the host fails with `EADDRNOTAVAIL`. The relay then reaches the proxy through `host.docker.internal`. On Linux, the daemon runs on the host, the bind succeeds, and the relay uses it.

## Name resolution in kind clusters

The kind plugin changes the cluster's CoreDNS configuration to forward the environment domain to the relay, then restarts CoreDNS. A pod resolves `<name>.<domain>` through the cluster DNS, with no proxy settings of its own.

A pod's request to a route then crosses two hops on the host: the pod connects to the relay over the project network, the relay forwards to the proxy on the host, and the proxy connects to the step's published port.

The nodes also use the relay for DNS outside `cluster.local`, through their own `/etc/resolv.conf`. This lets pods resolve [intercepted hostnames](#intercepted-hostnames).

## Traffic capture

kevin sends the traffic of every `builtin:container` step to the proxy, with no cooperation from the workload: no proxy variables and no DNS settings.

After a container starts, kevin calls the relay's `RegisterCapture` RPC with the path of the container's network namespace. The relay enters that namespace and adds an nftables table with an `output` NAT chain. Each rule redirects one port to the relay, for each address family the relay has an address in. The destination address no longer matters: a container that resolved the real IP of a third-party API reaches the relay the same way as one that resolved a route name.

The relay captures ports 80 and 443. An `intercept: true` route that lists other `ports` calls the relay's `EnsureListener` RPC. The relay opens a listener for each port and updates the capture rules of every registered container, so a route added after a container started still applies to it.

The relay reads the TLS server name or the HTTP `Host` header of a captured connection to find where it goes, then forwards it to the proxy.

### kind nodes

Pods run in the cluster's own network, which the relay cannot enter. A kind node, though, is an ordinary container. kevin registers each node once when the cluster starts. Every pod's traffic leaves through its node.

A node's rules differ from a container's. They use the `prerouting` hook, to capture traffic that passes through the node, not traffic the node sends itself. They skip the cluster's pod and service CIDRs, which kevin reads from kubeadm's `ClusterConfiguration`, so traffic between pods and to Services is not redirected. If kevin cannot read the CIDRs, it logs the error and does not add capture for that cluster. Wrong exclusions would break pod-to-pod traffic, which is worse than no capture.

## Intercepted hostnames

An `intercept: true` route also registers its hostname with the relay's DNS server. The relay answers with a synthetic address from `relay.intercept.ipv4_range` (default `198.18.0.0/15`, reserved for benchmark testing) or `relay.intercept.ipv6_range` (default a fixed ULA prefix). Each hostname gets a stable address.

A connection to a synthetic address is captured like any other. The relay reads the original destination of the connection (`SO_ORIGINAL_DST` on IPv4, and the IPv6 equivalent) and looks it up to find the route. It does not read the connection's bytes, so an intercept route can carry a TCP protocol that is not HTTP or TLS. Clash and sing-box use the same technique.

For a container, the DNS registration is not needed: capture by port already sends the traffic to the relay. For a pod, capture by port also works, but DNS through the relay is still needed to resolve names on the environment domain, which have no public DNS record.

## Tunnels from the host

The relay runs a SOCKS5 server. A client on the host can ask it to connect to an address inside the network. An `ExposedPort` that goes through the relay has an upstream of the form `socks5://127.0.0.1:<relayPort>/<address>` and `Relay: true`.

Most clients do not speak SOCKS5. For each relay `ExposedPort`, from any plugin, kevin opens a local listener and forwards each connection through the relay. For TCP it uses SOCKS5 `CONNECT`. For UDP it holds one SOCKS5 `ASSOCIATE` session open for the life of the listener. kevin adds the listener's address as `needs.<step>.system.forward_<name>`, next to `expose_<name>`, and the console shows both.

### kind clusters

A `builtin:kind` step's `expose` entries run a SOCKS5 relay as a pod in the cluster, from the `kevin-relay socks5-gateway` command. kind's `extraPortMappings` are fixed when the cluster is created, before `Up` knows which services exist. One relay pod needs one host port, whatever the number of services. `Up` chooses the port, adds one `extraPortMappings` entry for it on the control-plane node, loads the relay image into the node, and applies the pod with `kubectl` inside the node.

`Up` does not wait for an `expose` address to accept connections. The target usually comes from a manifest applied after the cluster starts. A [`builtin:wait`]({{< relref "/docs/reference/steps/wait" >}}) `tcp` check can dial the `expose_<name>` value to wait for it.

### Containers

A `builtin:container` `expose` entry with `relay: true` uses the project relay's own SOCKS5 server. No extra pod is needed, because the relay is already on the project network and resolves step names through the engine's DNS. This saves a host port for each entry. With few entries, a direct published port is simpler and has one hop fewer.

### UDP

SOCKS5 UDP `ASSOCIATE` (RFC 1928 section 7) normally binds a random port, which is known only after the relay container or pod exists, too late to publish it. `kevin-relay` binds a port from a fixed pool instead. The pool is `KEVIN_RELAY_UDP_POOL_SIZE` ports (default 16), published by the relay container, or reserved as host ports on a kind node. When the pool is full, a new session fails immediately. Set the size to `0` to reserve no ports.

Each UDP `ExposedPort` carries `RelayUdpAddrs`, which maps each pool port to its address on the host. The `ASSOCIATE` reply names a pool port, and kevin's local forward looks up the address there. RFC 1928 ties a session to its TCP control connection, so if that connection drops, kevin closes the local listener.

A local UDP forward sends each reply to every client that used it recently. SOCKS5 tracks one peer for each session, so it cannot separate flows. This works for a few tools that share one port, not for full per-flow isolation.

## Routes through the relay

A `builtin:route` entry names either an address the proxy can dial, such as a container's published port, or, with `relay` set, an address inside a kind cluster.

For a relay route, the step returns a `Route` whose upstream uses the `socks5://` form above. When the proxy dials an upstream of that form, it connects to the relay and sends a SOCKS5 `CONNECT` for the real address. WebSocket upgrades use the same dial path.

`builtin:route` does no Kubernetes work. It takes a relay address and a list of host and address pairs. A kind step starts the relay pod when it has `expose` entries or `relay: true`, and reports its address as `relay_addr`. The route's `needs` must include the step that deploys the target, so the target exists when the route starts.

## Control channel

kevin controls the relay over gRPC with mutual TLS. kevin signs a short-lived server certificate for the relay and a client certificate for itself, both from the project CA (see [Certificate authority]({{< relref "/docs/concepts/ca" >}})), and passes the server certificate and root to the relay container. Only a client with a certificate from this project's CA can change the relay's DNS, listeners, capture, or faults. The private key of that CA is in kevin's state directories.

## Fault injection

`builtin:fault` uses the same access as capture. The relay opens a target's `/proc/<pid>/ns/net` and adds a Linux `netem` qdisc to an interface in that namespace, with `github.com/vishvananda/netlink`. The RPCs are `ApplyFault` and `ClearFault`, on the same control channel.

Faults apply to the interface, so they affect all traffic, whether or not it goes through capture, a route, or the proxy. kevin applies a fault only for a `builtin:fault` step, and removes it on teardown.

## Limits

### Privileges

To change another container's network namespace, the relay container has `CAP_NET_ADMIN` (to add rules), `CAP_SYS_ADMIN` (to enter the namespace), and `CAP_SYS_PTRACE`, and shares the host PID namespace (`--pid host`). It opens a target's namespace at `/proc/<pid>/ns/net`, from the PID that `docker inspect` reports.

A bind mount of `/var/run/docker/netns` did not work: on OrbStack, a namespace file created after the mount can be read, but entering it fails with `EINVAL`. `/proc/<pid>/ns/net` always shows the current host PID namespace, so start order does not matter. `CAP_SYS_PTRACE` is needed to open the namespace of a `--privileged` container, such as a kind node, because the kernel marks its processes non-dumpable.

These capabilities let a compromised relay enter the namespace of any process on the host. The relay acts only on paths that kevin sends over the mTLS control channel, which limits this in practice. kevin accepts this risk for its threat model: one local developer, whose kevin CA can already read the project's TLS traffic.

### Protocols

Capture matches TCP only. UDP and QUIC are not captured, and a client that uses HTTP/3 connects directly. The SOCKS5 tunnels do carry UDP. Capture and the tunnels are separate features in the same process.

### Egress

Capture sends traffic to the proxy. The allow list decides whether the proxy lets it through. Traffic on ports that are not captured goes directly to its destination.

### Published ports

The proxy on the host still reaches a workload through the port that the container plugin publishes on the loopback address, because the proxy cannot resolve a network alias.
