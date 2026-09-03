---
title: "Scopes and providers"
description: "The two DAG scopes, the provider model, and where a plugin binary comes from."
weight: 2
---

# Scopes and providers

A project directory holds a `kevin.cue` file. This file declares the plugins and the steps. kevin checks the file against a core schema before anything runs.

The environment file holds two independent DAGs, and each DAG is a scope.

| Scope   | Lifetime             | Commands                        |
|---------|----------------------|---------------------------------|
| `setup` | Pesists across runs. | `kevin setup`, `kevin teardown` |
| `env`   | Ephemeral.           | `kevin run`                     |

The two scopes use one engine and one protocol. An `env` step's `needs` may additionally name a `setup` step, prefixed `setup.` (`needs: ["setup.<name>"]`) - resolved through `Export`, not `Up`, since a plain `kevin run` never brings the setup scope up in that process. See [Cross-step values]({{< relref "/docs/concepts/cross-step-values" >}}).

State for one project lives in a `.kevin/` folder, or `.kevin/<name>/` for a named environment selected with `-e`/`--env` (see [Environment file]({{< relref "/docs/environment-file#file-name-and-format" >}})). Two projects, or two named environments in one project directory, can run at the same time, because kevin prefixes every resource with the project name.

## Provider model

A plugin is a provider. A provider offers one or more step types, and a step names one with `uses: "<plugin>:<step>"`. Both parts are required: the plugin that offers the step type, and the step type itself.

`builtin` is the provider that kevin supplies. It is never declared in a `plugins:` block. See [Reference]({{< relref "/docs/reference" >}}) for the step types it currently offers.

Nothing about the engine is specific to any one step type, builtin or third-party. A Kubernetes cluster, for instance, is a plugin like any other, not a special case: the first one uses [kind]({{< relref "/docs/guides/kubernetes" >}}), and a plugin for minikube or k3s would be a new binary needing no change to the engine.

A `plugins:` entry names a source, not a namespace. A source says how kevin obtains the plugin binary. `cmd`, `file`, `oci`, and `http` are the sources that kevin implements. `file` names a tar package: a manifest, the plugin binary, and any supporting files, extracted into the project workspace and launched exactly like a `cmd` source. `oci` names the same package, fetched from an OCI registry with multi-arch image-index resolution. `http` names the same package again, fetched over a plain URL with an optional `checksum` pin, since a URL carries no built-in digest addressing the way an OCI reference does. `oci` and `http` share one global, content-addressed cache under `~/.kevin/pkg-cache/`. The same sha256-named blob downloaded by either source is available to the other, so a package already fetched by one extracts the same way when the other one references it.

`file`, `oci`, and `http` also accept `signed`, which pins provenance instead of (or alongside) a `checksum`/digest pin: it requires a valid [minisign](https://jedisct1.github.io/minisign/) signature from a key in a local trust store (`~/.kevin/trusted-keys/`) before the package is ever extracted. Verification (Ed25519 plus BLAKE2b-512 prehashing, through minisign's own author's reference Go implementation) is the only signing code kevin ships. Signing itself is always the external `minisign` CLI, applied to the archive `kevin plugin pack` built, so kevin's binary never holds or generates secret key material. The trust store lives outside `kevin.cue` deliberately: `checksum` and a plugin's declared `oci`/`http` reference already live in the same file a project author controls, so pinning trust there too would let anyone who can edit `kevin.cue` also choose what's trusted. `kevin plugin trust add/list/remove` manage the store; `kevin plugin push` uploads a package's sibling `.minisig` file automatically, via a cosign-style fallback tag (`<repo>:sha256-<digest>.sig`). The OCI client kevin uses has no OCI 1.1 referrers-API support, so this reuses the same tag/manifest/blob calls the package itself already goes through, rather than a dedicated discovery mechanism.

A `plugins:` entry may carry a `config` block of its own. This block configures the provider, not one step. The provider validates it against its own config schema.

Fourteen namespaces are reserved:

`builtin`, `cmd`, `core`, `docker`, `file`, `helm`, `http`, `k8s`, `kevin`, `kubectl`, `kubernetes`, `oci`, `official`, `std`.

A `plugins:` key cannot declare one of them, so that no third-party plugin reads as first-party, and so that a namespace is never mistaken for a source.

Only a plugin that a step references starts. A `plugins:` entry that no step names declares no binary and starts nothing.
