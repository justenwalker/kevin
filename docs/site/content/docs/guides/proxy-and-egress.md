---
title: "Proxy and egress"
description: "Reach a step through the proxy, give it a name, and control outbound traffic."
weight: 1
---

# Proxy and egress

kevin does not change files on your machine, such as `/etc/hosts`. You reach the services of an environment through the kevin proxy.

## Set the proxy and console addresses

Every environment file sets both addresses:

```cue
proxy: {
    listen:       "127.0.0.1:18080"
    gateway_port: 18082
}
console: listen: "127.0.0.1:18081"
```

`gateway_port` is the port that containers use to reach the proxy. Choose ports that are free on your machine. kevin does not choose them for you, so the addresses stay the same across runs.

## Give a service a name

Add a [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) step:

```cue
env: {
    web: {
        uses: "builtin:container"
        with: {image: "nginx:alpine", expose: web: {port: 80}}
    }
    web_route: {
        uses:  "builtin:route"
        needs: ["web"]
        with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
    }
}
```

The service is now `web.kevin.home`. To use a different base domain, set `domain:` in the environment file.

## Reach a service from the host

Use one of these:

- **One command.** Pass the proxy to the client:

  ```sh
  curl --proxy http://127.0.0.1:18080 https://web.kevin.home/
  ```

- **A shell.** Set the proxy variables:

  ```sh
  export HTTP_PROXY=http://127.0.0.1:18080 HTTPS_PROXY=http://127.0.0.1:18080
  ```

- **A browser.** Set the proxy auto-config URL to `http://127.0.0.1:18080/proxy.pac`. The browser sends the environment domain through the proxy and all other traffic directly.

HTTPS needs the kevin CA. See [Trust the kevin CA]({{< relref "ca-and-trust" >}}).

## Block outbound traffic

Set `proxy.egress.deny` and list the hosts that steps can reach:

```cue
proxy: egress: {
    deny:  true
    allow: ["api.github.com", "*.docker.io", "docker.io"]
}
```

A wildcard such as `*.docker.io` does not match `docker.io`, so list both if you need both.

To allow a host for one step only, use the `egress` field of that step, such as on [`builtin:container`]({{< relref "/docs/reference/steps/container" >}}) or [`builtin:kubernetes`]({{< relref "/docs/reference/steps/kubernetes" >}}).

If a step fails to start because the proxy blocked a host it needs, kevin prints a warning that names the hosts:

```text
cluster          warning: the proxy denied requests to registry-1.docker.io while cluster was starting; to allow them, add the hosts to proxy.egress.allow in kevin.cue
```

Add those hosts to `proxy.egress.allow`, not to the `egress` field of the step, then start the environment again.

A blocked request gets a `403` page that names the host and the CUE to add. The console shows the blocked request.

To allow all outbound traffic, set `deny: false`.

To switch `deny` on the command line, see [Per-machine and per-run settings]({{< relref "local-and-per-run-settings" >}}).

## Reach an allowed host without trusting the kevin CA

By default, the proxy terminates TLS for every host, so a client that does not trust the kevin CA fails to connect, even to an allowed host. Clients such as `git`, `pip`, or a Go program often use only the system trust store.

Set `passthrough` to send TLS to allowed hosts unchanged:

```cue
proxy: egress: {
    deny:        true
    allow:       ["api.github.com"]
    passthrough: true
}
```

The client then checks the real certificate of the host. A blocked host still gets a `403`. The console shows one entry for each connection, with no request details.

`passthrough` applies only to hosts with no route.

## Test the TLS certificate of your service

If your service has its own certificate, such as one from cert-manager in a kind cluster, set `mode: "passthrough"` on its route:

```cue
routes: [{host: "myapp", address: "myapp.default.svc.cluster.local:443", tls: true, mode: "passthrough"}]
```

The client then checks the certificate of your service, not a kevin certificate.

## Route a protocol that is not HTTP

For a TCP protocol such as a database protocol, set `mode: "raw"`:

```cue
routes: [{host: "db", address: "${needs.db.out.host_5432}", mode: "raw"}]
```

The proxy forwards the connection unchanged. A client connects with an HTTP `CONNECT` request to the proxy for `db.kevin.home:5432`.

## Related

- [Proxy]({{< relref "/docs/concepts/proxy" >}}): how the proxy works.
- [Environment file: proxy]({{< relref "/docs/reference/environment-file#proxy" >}})
