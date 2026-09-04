---
title: "Proxy and egress"
description: "Reach a step through the proxy, and control what it can reach on the way out."
weight: 1
---

# Proxy and egress

kevin changes no file on the host: no entry in `/etc/hosts`, no file in `/etc/resolver`, no DNS server for the host. You reach the environment through the proxy instead.

## Reaching a step

Set `HTTP_PROXY`/`HTTPS_PROXY`, or point a browser at `http://<proxy>/proxy.pac`. The auto-config file sends the environment domain through the proxy and everything else direct, so normal browsing is untouched, and it updates automatically as steps are added (no reload needed).

The environment has a base domain, `kevin.home` by default; set `domain:` in `kevin.cue` to use a different one. A [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) step puts a name on it, pointed at a `builtin:container` step's published port for a direct address, or at a relay for a target the proxy can't dial itself, such as a Service inside a `builtin:kind` cluster. A bare step name alone is never routable; it always needs the dot and the domain, so a step name can't accidentally shadow a real host on the internet.

`proxy: listen:` in `kevin.cue` sets the proxy's primary, host-facing address, such as `"127.0.0.1:18080"` - kevin picks no port for you, and requires a real one: a `builtin:kind` step bakes this address into every node's containerd config at creation time, so a cluster left running by `kevin setup` needs it stable across a later process picking a different one. The web console has the same knob, `console: listen:`, and requires a real port for the same reason: kevin never auto-assigns a listener address, so a bookmark, a script, or anything else that expects the console at a fixed place stays valid across runs.

The proxy also binds a second listener, on the docker network's gateway address, for the relay to reach it from inside the network. `proxy: gateway_port:` sets that listener's port - also required, for the same reason as `listen`.

A `builtin:kind` cluster's reuse check folds the resolved proxy address into its comparison, so a cluster created against one `proxy: listen:` value is never silently reused against another - changing it (or toggling the proxy on and off) forces a fresh cluster instead of leaving nodes dialing a dead address.

`NO_PROXY` lists the step names too, so a client that honors it reaches another step directly over the docker network. Not every client does (busybox `wget` ignores `NO_PROXY`, for example), so a step is always also reachable through the proxy under its full `<step>.<domain>` name.

The proxy terminates TLS for you: it mints a leaf certificate for the requested host and signs it with the kevin CA (see [CA and trust store]({{< relref "ca-and-trust" >}})), so `curl --cacert` or a machine that already trusts the kevin root just works. The proxy negotiates both HTTP/1.1 and HTTP/2 with the client over that connection.

## Egress control

`proxy: egress: deny:` has no schema default - `kevin.cue` must set it to `true` or `false` itself:

```cue
proxy: egress: {
	deny:  true
	allow: ["api.example.com"]
}
```

With `deny: true`, a workload reaches the internet only through a host `kevin.cue` names. A denied host gets a `403` page naming the host and the exact CUE to add. An allow entry is an exact host, such as `api.github.com`, or a leading-dot wildcard, such as `*.github.com`. A wildcard matches a subdomain but not the bare domain, so list both when both need to be reachable. Matching ignores case and any port.

`proxy: egress: allow` in `kevin.cue` names hosts for the whole environment. A step can also open hosts for itself alone through its own plugin (the `egress` field on `builtin:container` and `builtin:kind`, for example). See [Reference]({{< relref "/docs/reference" >}}) for each step type's `with` block.

Set `proxy: egress: deny: false` instead, for an environment that needs no such protection - every request then reaches the internet.

The `403` page carries cache-busting headers, so a browser won't keep showing a stale denial after you fix the allow list.

It also flips cleanly per run - attach `@tag` straight to it:

```cue
package kevin

proxy: egress: deny: bool @tag(airgap,type=bool)
```

Then flip it per run with `kevin run -t airgap` instead of duplicating the whole file into a named environment just to change one field. See [`@tag` mode switches]({{< relref "/docs/environment-file#tag-mode-switches" >}}) for tagging a field that already has a fallback value, and for sharing one toggle across more than one field.

## Step readiness

A container reports `Running` before the process inside has necessarily bound its port. A TCP `expose` entry is only actually reachable once its published port accepts a connection. kevin waits for that before marking the step ready. Note this if you're debugging a race in your own tooling against a step.
