---
title: "Scopes and providers"
description: "The two scopes of steps, the provider model, and where a plugin comes from."
weight: 2
---

# Scopes and providers

A project directory has an environment file, `kevin.cue`, that declares plugins and steps. kevin checks the file against its core schema before anything runs.

## Scopes

The environment file holds two independent graphs of steps, called scopes.

| Scope | Lifetime | Commands |
|:------|:---------|:---------|
| `setup` | Stays after the command exits. | `kevin setup`, `kevin teardown` |
| `env` | Removed when `kevin run` exits. | `kevin run` |

`setup` holds things that are slow to create and shared by many runs, such as a Kubernetes cluster. `env` holds things that are restarted often. Both scopes use the same engine and protocol. An `env` step can read the outputs of a `setup` step. See [Cross-step values]({{< relref "/docs/concepts/cross-step-values#crossing-scopes" >}}).

The state of a project is in `.kevin/`, or `.kevin/<name>/` for a named environment. Every resource name starts with the project name. Two projects, or two named environments in one directory, can run at the same time.

## Providers

A plugin is a provider. It offers one or more step types, and a step names one as `uses: "<plugin>:<step>"`.

`builtin` is the provider that ships in kevin. It has no `plugins:` entry. See [Steps]({{< relref "/docs/reference/steps" >}}) for its step types.

The engine has no code for any specific step type. A Kubernetes cluster is a plugin like any other: the builtin one uses kind, and a plugin for minikube or k3s would be a new binary with no change to the engine.

## Plugin sources

A `plugins:` entry names a source: how kevin gets the plugin.

- `cmd` is a binary on disk.
- `file` is a package: a tar archive with a manifest, the binary, and any files it needs. kevin extracts it into the project's `.kevin/plugins/<name>/` and runs it like a `cmd` binary.
- `oci` is the same package in an OCI registry. For a multi-architecture package, kevin selects the host's platform.
- `http` is the same package at a URL. A URL has no digest, so `http` accepts a `checksum` to pin the content.

`oci` and `http` share one cache in `~/.kevin/pkg-cache/`, keyed by the SHA-256 of the package. A package fetched by one source is reused by the other.

A package can require a signature. See [Plugin trust]({{< relref "/docs/concepts/plugin-trust" >}}).

A `plugins:` entry can have a `config` block. It configures the provider, not one step, and the provider checks it against its own schema.

## Reserved names

Some names, such as `builtin`, `kevin`, and `oci`, cannot be a `plugins:` key (see the [full list]({{< relref "/docs/reference/environment-file#plugins" >}})). This keeps a third-party plugin from looking like part of kevin, and keeps a plugin name from looking like a source.

## Which plugins start

kevin starts a plugin only when a step uses it. An entry that no step uses starts nothing.
