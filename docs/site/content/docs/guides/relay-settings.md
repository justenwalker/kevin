---
title: "Relay settings"
description: "Reach container ports without publishing them, and use a mirrored relay image."
weight: 8
---

# Relay settings

Every environment runs a relay container on the project network. See [Relay]({{< relref "/docs/concepts/relay" >}}) for what it does.

## Reach a container port without publishing it

Each `expose` entry of a [`builtin:container`]({{< relref "/docs/reference/steps/container" >}}) step normally uses a host port. To reach the port through the relay instead, set `relay: true`:

```cue
db: {
    uses: "builtin:container"
    with: {
        image:  "postgres:16"
        expose: postgres: {port: 5432, relay: true}
    }
}
```

The step's `forward_postgres` system value is a `127.0.0.1:<port>` address. The console shows it. Any client can connect to it:

```sh
psql -h 127.0.0.1 -p <port> -U postgres
```

`relay` works with `protocol: "tcp"` and `protocol: "udp"`. A step can have both kinds of entries.

## Use a mirrored relay image

By default, kevin pulls the relay image from `ghcr.io/justenwalker/kevin/relay`, with a tag that matches the kevin version. To use a copy in your own registry, set one of:

| Setting | Changes |
|:--------|:--------|
| `relay: image:` in the environment file | The full image reference. |
| `KEVIN_RELAY_IMAGE` | The full image reference. Overrides `relay: image:`. |
| `KEVIN_RELAY_REPO` | Only the repository of the image that kevin would otherwise use. |
| `KEVIN_RELAY_TAG` | Only the tag of the image that kevin would otherwise use. |

```sh
export KEVIN_RELAY_REPO=registry.example.com/mirror/kevin-relay
```
