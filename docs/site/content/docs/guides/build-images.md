---
title: "Build an image"
description: "Build a container step's image from a local Dockerfile."
weight: 11
---

# Build an image

This guide builds the image of a `builtin:container` step from a Dockerfile in your project, then runs it.

## Build from a Dockerfile

Replace `image` with `build`. Set exactly one of the two.

```cue
env: api: {
    uses: "builtin:container"
    with: build: context: "./api"
}
```

A relative `context` is relative to the project directory. The step builds `Dockerfile` in the context.

## Choose a Dockerfile, arguments, and stage

```cue
env: api: {
    uses: "builtin:container"
    with: build: {
        context:    "./api"
        dockerfile: "Dockerfile.dev"
        args: GO_VERSION: "1.25"
        target:     "dev"
    }
}
```

`dockerfile` is relative to `context`. Each entry in `args` is a build argument.

## Set the user and limits

```cue
env: api: {
    uses: "builtin:container"
    with: {
        build: context: "./api"
        user:    "1000:1000"
        workdir: "/app"
        cpus:    "1.5"
        memory:  "512m"
    }
}
```

These fields work with `image` too.

## Read the build output

The build output appears in the step's log in the console. The step shows `building`, then `starting`, so you can see which phase is slow.

## Rebuild after a change

Every start of the step builds the image again. To rebuild while `kevin run` is up, run:

```sh
kevin rerun api
```

To rebuild when files change, see [Rerun on change]({{< relref "/docs/guides/rerun-on-change" >}}).

## Remove built images

Teardown keeps the built image, named `kevin-<project>-<step>:latest`. Remove it with your engine:

```sh
docker image rm kevin-my-project-api:latest
```

For how the build relates to egress control, see [Proxy]({{< relref "/docs/concepts/proxy#which-traffic-reaches-the-proxy" >}}).
