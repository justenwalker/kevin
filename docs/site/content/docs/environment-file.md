---
title: "Environment File"
weight: 2
---

# Environment file

An environment is a `kevin.cue` (or `.yaml`/`.yml`/`.json`, or a `.`-prefixed dotfile variant of any of those - see [File name and format](#file-name-and-format) below) in the project directory. It declares the steps that make up the environment, and the plugins that a step outside kevin needs. A CUE file may declare a `package` clause; see [Package-mode splitting](#package-mode-splitting) below.

A plugin is a provider: it offers one or more step types under its own name. A step's `uses` field names a step type as `<plugin>:<step>`. `builtin` is the provider kevin supplies, and it needs no `plugins:` entry. See [Reference]({{< relref "/docs/reference/steps" >}}) for every step type it offers and each one's `with` block.

A step also takes an optional `label`, a friendly name for the console. The step's own key still names it everywhere else (`needs`, the domain, the event log).

```cue
project: "my-app"

env: {
    web: {
        uses: "builtin:container"
        with: image: "nginx:alpine"
    }
}
```

A step that names a plugin kevin does not ship needs a `plugins:` entry whose key is the plugin name. The entry may carry a `config` block, which kevin delivers to the plugin once, before any of its steps run:

```cue
project: "my-app"

plugins: echo: {
    cmd:    "./bin/kevin-plugin-echo"
    config: greeting: "hello"
}

env: {
    db: {
        uses: "echo:echo"
        with: message: "starting the database"
    }
    api: {
        uses: "echo:echo"
        needs: ["db"]
        with: message: "starting the API"
    }
}
```

A `plugins:` entry names a source, not a namespace. It says how kevin obtains the plugin binary. `cmd` (a path to a binary, as above), `file` (a package tar, below), `oci` (the same package tar, fetched from a registry, below), and `http` (the same package tar again, fetched over a plain URL, below) are the sources kevin implements.

## Plugin sources

### `file`

`file` points at a tar archive, optionally gzip-compressed, holding the plugin binary, a `manifest.json` at its root, and any supporting files the binary needs alongside it:

```cue
plugins: echo: file: "./dist/kevin-plugin-echo.tar.gz"
```

```json
{
    "$v": 1,
    "name": "echo",
    "version": "1.0.0",
    "entrypoint": "kevin-plugin-echo"
}
```

`name` must match the `plugins:` key the package is configured under. `entrypoint` names the executable inside the archive; `args`, if the manifest sets them, are the binary's default arguments, and a `plugins:` entry's own `args` field replaces them entirely when set. Environment variables never come from the manifest. Set them with `env`, the same as a `cmd` source. A `checksum` field pins the archive's sha256, checked before anything inside it is read:

```cue
plugins: echo: {
    file:     "./dist/kevin-plugin-echo.tar.gz"
    checksum: "sha256:..."
}
```

kevin extracts the archive once per project into `.kevin/plugins/<name>/`, and skips re-extraction on a later run when the archive is unchanged.

`kevin plugin pack <dir>` builds this archive: point it at a directory holding the entrypoint binary and any supporting files, and it writes the above `manifest.json` and tars everything into a `.tar.gz`, e.g. `kevin plugin pack ./dist/echo -o ./dist/kevin-plugin-echo.tar.gz --name echo --version 1.0.0 --entrypoint kevin-plugin-echo`. A `manifest.json` already in `dir` is used as a base; the flags override or fill in its fields.

### `oci`

`oci` points at an OCI artifact whose single layer is the exact same tar archive the `file` source extracts. kevin does not run the reference as a container; it fetches one content-addressed blob from the registry:

```cue
plugins: echo: oci: "ghcr.io/acme/kevin-plugin-echo:v1"
```

A tag or a pinned digest both work; a digest needs no separate `checksum` field, since `host/repo@sha256:...` already pins the exact bytes:

```cue
plugins: echo: oci: "ghcr.io/acme/kevin-plugin-echo@sha256:..."
```

A multi-arch image index resolves to the manifest for the host's own OS/architecture. `args`, `env`, and `config` work the same as the `file` source. kevin caches a fetched blob once, globally, under `~/.kevin/pkg-cache/`, keyed by its digest, so the same immutable blob referenced by several projects, or fetched by `oci` in one project and `http` in another, downloads once. It then extracts into `.kevin/plugins/<name>/` exactly like `file` does. Authentication reuses whatever `docker login` already wrote to `~/.docker/config.json`.

`kevin plugin push <tar.gz> <ref>` publishes a package built by `kevin plugin pack` to a registry, e.g. `kevin plugin push ./dist/kevin-plugin-echo.tar.gz ghcr.io/acme/kevin-plugin-echo:v1`. It reuses the same `docker login` credentials.

### `http`

`http` points at a plain URL serving the exact same tar archive the `file` source extracts:

```cue
plugins: echo: http: "https://example.com/kevin-plugin-echo.tar.gz"
```

Unlike `oci`, a URL carries no built-in digest addressing, so `checksum` is the only *content*-pinning option `http` has. `signed` pins provenance instead; see [Signing packages](#signing-packages) below:

```cue
plugins: echo: {
    http:     "https://example.com/kevin-plugin-echo.tar.gz"
    checksum: "sha256:..."
}
```

`args`, `env`, and `config` work the same as the other package sources. `http` shares the `oci` source's cache (`~/.kevin/pkg-cache/`), so a `checksum`-pinned URL that's already cached, whether it got there via `http` or `oci`, skips the download entirely. Without a `checksum`, kevin has no digest to check ahead of time, so it always re-downloads, though it still caches the result afterward for later reuse.

### Signing packages

`file`, `oci`, and `http` all support `signed`, which pins provenance instead of (or alongside) `checksum`. Rather than pinning one archive's exact bytes, it requires a valid [minisign](https://jedisct1.github.io/minisign/) signature from a key in the local trust store. This suits a moving `oci` tag, or a plugin author who cuts releases faster than `kevin.cue` gets updated.

Sign the archive `kevin plugin pack` built:

```sh
minisign -Sm ./dist/kevin-plugin-echo.tar.gz
```

This writes `kevin-plugin-echo.tar.gz.minisig` next to the archive. `kevin plugin push` picks it up automatically and publishes it alongside the package; for `file` and `http`, kevin looks for the same `<archive>.minisig` sibling (or `<url>.minisig`, for `http`).

On the consuming side, add the signer's public key to the trust store, `~/.kevin/trusted-keys/`. This store is global and lives outside `kevin.cue` on purpose, so editing the project file alone can never add a trusted signer. Then opt the plugin in:

```sh
kevin plugin trust add ./signer.pub
```

```cue
plugins: echo: {
    oci:    "ghcr.io/acme/kevin-plugin-echo:v1"
    signed: true
}
```

`kevin plugin trust list` shows the keys currently trusted, and `kevin plugin trust remove <key-id>` retires one. A package with no valid signature from a trusted key fails closed: kevin refuses to extract it.

Only a plugin that a step references starts. `kevin plugin list` prints every builtin name.

## File name and format

kevin validates the environment file against a schema, so CUE syntax is convenience, not a requirement: YAML and JSON work the same way, and decode to the same result. kevin picks the format from the file's extension.

The unnamed environment is `kevin.cue`, `kevin.yaml`, `kevin.yml`, or `kevin.json`, or the same names with a leading `.` (`.kevin.cue`, and so on) for folks who'd rather keep it out of a plain directory listing. Exactly one of these may exist in the project directory.

A project directory can also hold more than one environment, each with its own name. Pass `--env`/`-e` (every subcommand accepts it, including `kevin do`), or set `KEVIN_ENV` in the shell to avoid repeating the flag, to select `<name>.kevin.<ext>` or `.<name>.kevin.<ext>` instead:

```sh
kevin -C . --env staging run
```

looks for `staging.kevin.cue` (or `.staging.kevin.cue`, `staging.kevin.yaml`, ...). Two named environments in the same directory are fully independent: each gets its own default `project` (the directory name plus the environment name, e.g. `my-app-staging`, unless `project:` is set explicitly), its own Docker network, its own CA/TLS state, and its own `./.kevin/<name>/` workspace - so `kevin -C . run` and `kevin -C . --env staging run` can be up at the same time.

`KEVIN_PROJECT_STATE_DIR` overrides that per-project workspace path outright (skipping the `.kevin`/`<name>` join). `KEVIN_USER_STATE_DIR` does the same for the user-wide state directory, `~/.kevin`, which holds the trust store and package cache described below.

### Package-mode splitting

A `kevin.cue` may declare a `package` clause. When it does, every other `.cue` file in the same directory sharing that clause unifies with it - CUE's own multi-file-per-package model, no `import` statement and no `cue.mod` required:

```cue
// kevin.cue
package kevin

project: "my-app"
env: web: {uses: "builtin:container", with: image: "nginx:alpine"}
```

```cue
// mirrors.cue
package kevin

plugins: registry: config: mirror: "https://mirror.internal"
```

`kevin -C .` loads both files as one environment. A `.cue` file in the directory *without* a matching `package` clause is a hard error naming the conflict - kevin never silently drops half a package-mode environment because one file forgot its clause. YAML and JSON candidates can't declare a package and always load alone; a legacy-format file sitting in an otherwise package-mode directory is the same hard error.

Machine-specific facts that shouldn't go into source control - local plugin credentials, a locally-adjusted port - now live in a plain, developer-named `.cue` file, gitignored, carrying the same `package` clause as the rest of the directory:

```sh
echo 'kevin.local.cue' >> .gitignore
```

```cue
// kevin.local.cue
package kevin

plugins: registry: config: token: "local-dev-token"
```

This is the one place kevin does *not* guard against a missing clause: a gitignored file the developer owns that itself forgets `package kevin` is silently excluded from the build, not an error - the same behavior CUE's own multi-file-package model gives any file that isn't gitignored, applied here to a file no one else's build depends on.

### `@tag` mode switches

A mode toggle - `airgap`, a feature flag, anything you'd otherwise flip by hand-editing `kevin.cue` before a run - is what `--tag`/`-t` is for. Mark the field with `@tag(<name>,type=bool)` and pass `-t <name>` on the command line:

```cue
package kevin

proxy: egress: deny: bool @tag(airgap,type=bool)
```

```sh
kevin run -t airgap
```

Without `-t airgap`, the field is whatever it would normally be. With it, the field becomes `true`. This works exactly the same whether the field already had a fallback value written into `kevin.cue` or not - `@tag` on a field is enough by itself, nothing else in the file needs to change to make a field taggable.

`--tag`/`-t` is repeatable, only works against a package-mode file (see above - a legacy `kevin.cue` with no `package` clause can't take a tag, and kevin says so rather than silently ignoring the flag), and takes more than booleans: a bare `-t airgap` is shorthand for `-t airgap=true`, and `-t key=value` sets a string, int, or number field the same way.

#### Sharing one toggle across several fields

Tagging a field directly is enough when only that one field needs to change. If the same on/off switch should also gate something else - a second field, a condition, text spliced into a log message - give it its own name instead, and reference that name everywhere you need it:

```cue
package kevin

airgap: bool | *false @tag(airgap,type=bool)
if airgap {
	domain: "airgap.kevin.home"
}
```

`airgap` is a plain field here, taggable the same way as before, but now readable anywhere else in the file too.

**One thing that looks like it should work and doesn't**: if the field you're changing already has a fallback value in `kevin.cue` (`domain` above normally falls back to `kevin.home`), don't point it straight at your toggle field, like `domain: *airgap | string`. It looks like a natural way to say "normally kevin.home, but let `airgap` decide otherwise" - instead, kevin quietly keeps the original `kevin.home` fallback no matter what `-t airgap` is set to, and nothing warns you this happened. Use the `if` block shown above instead: it always writes a real, specific value for `domain` when `airgap` is true, rather than trying to swap out one fallback for another. This is exactly why `proxy.egress.deny`, `proxy.listen`, `proxy.gateway_port`, and `console.listen` have no built-in fallback of their own in kevin's schema - fields without one can be tagged directly with no `if` block needed, which is one less thing to get wrong.

Both forms can be combined - tag a field directly for the parts that only need that one value, and keep a named field only for the parts that need it reused elsewhere. The same `-t` flag drives both at once.

## Scopes

Steps live in one of two scopes. `setup` steps persist across runs and are managed by `kevin setup` and `kevin teardown`. `env` steps are ephemeral: `kevin run` brings them up and removes them again on exit. Pass `--keep` to leave them in place on exit instead, for example to inspect a container after a failure.

## Validating

`kevin validate` loads the environment file, starts the declared plugins, and checks every step's `with` block against its plugin's schema: everything `run`/`setup` do before touching Docker. It creates nothing and needs no Docker daemon.

`kevin init` downloads and extracts each `plugins:` entry a step actually uses, verifying its signature if `signed: true` is set. It starts no plugin process and checks nothing against a schema.

## Ordering

A step's `needs` list names the steps it depends on. Independent steps come up in parallel; a step waits only for what it actually needs. If a step fails, kevin cancels whatever hasn't started yet, then removes everything that did come up, in reverse dependency order. A failure partway through never leaves orphan containers behind.

```sh
kevin -C examples/echo run
```

[`examples/echo`](https://github.com/justenwalker/kevin/tree/main/examples/echo) demonstrates this end to end with no real resources: `a` runs first, `b` and `c` run in parallel once `a` is up, `d` waits for both, and a `boom` step fails on purpose so its dependent never runs. kevin then removes `a` through `d` in reverse and exits non-zero.

## Cross-step values

A step publishes outputs, such as the endpoint of a registry or the path of a kubeconfig file. Every step that declares a `needs` edge on that step gets them, both directly (a plugin author reads them off the wire request; see [The plugin protocol]({{< relref "/docs/extending/plugin-protocol" >}})) and inside its own `with` block, using a `${cel-expression}`:

```cue
env: {
    cluster: uses: "builtin:kind"
    app: {
        uses: "builtin:kubectl"
        needs: ["cluster"]
        with: {
            kubeconfig: "${needs.cluster.out.kubeconfig}"
            context:    "${needs.cluster.out.context}"
        }
    }
}
```

Any string in a `with` block, at any depth, that contains `${...}` gets that expression evaluated against a `needs` variable keyed by upstream step name, and the result spliced back into the surrounding text. A `with` block that never uses `${` pays no cost. This is how the [kubectl and helm steps]({{< relref "guides/deploying-workloads" >}}) read a cluster's `kubeconfig`/`context`, and it works the same way for any plugin's `with` block, not just the builtin ones.

`kevin validate` checks a `${needs.<step>...}` or `${setup.<name>...}` reference against the step's own `needs` list statically - both are just text in the file - so a step name missing from `needs` fails validate with a clear error instead of surfacing later as a "no such key" failure once that step's `with` block actually renders.

Each step entry under `needs` has two sub-namespaces: `out` (that step's own plugin-authored outputs, as above) and `system` (values kevin computes itself, currently `expose_<name>`/`forward_<name>` for a step's `ExposedPort` entries). They're kept apart so a kevin-computed key can never collide with one a plugin chose for its own output: `${needs.cluster.system.forward_postgres}` reads the same way `${needs.cluster.out.kubeconfig}` does; see [Cluster tunnel]({{< relref "/docs/concepts/relay#cluster-tunnel" >}}).

The same `${...}` expression can also read `env.<VAR>`, kevin's own environment variables, for example `with: registry: "${env.REGISTRY_HOST}"`. Referencing a variable that isn't set is an error; give it a fallback with `${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : "localhost:5000"}`. `project.<key>` reads a small fixed set of project-level constants instead - kevin's CA paths and its own proxy address - for a tool that only takes them as a command-line flag, such as `${project.root_cert}` for `curl --cacert`. See [CEL expressions]({{< relref "/docs/reference/cel-expressions" >}}) for the full syntax reference.

A plugin can mark one of its own outputs sensitive - a generated password, for example - which keeps the engine from ever writing it to a log or the console card in full. A `${needs.<step>.out.<key>}` expression still reads a sensitive value's content, since it's substituted into a downstream step's own `with` block, not displayed; that downstream step must mark it sensitive again itself if it echoes the value back out as one of its own outputs or console details.

### Reading a `setup` step's value

`needs` normally only reaches a step in the *same* scope. An `env` step can additionally name a `setup` step, prefixed `setup.`, since `kevin setup` typically provisions something once that many later `kevin run`s want to reuse - a kind cluster, say:

```cue
setup: cluster: uses: "builtin:kind"
env: app: {
    uses:  "builtin:kubectl"
    needs: ["setup.cluster"]
    with:  kubeconfig: "${setup.cluster.out.KUBECONFIG}"
}
```

Two things differ from a same-scope reference. First, the `needs` entry itself carries the `setup.` prefix - only a bare, unprefixed name ever means "a step in my own scope." Second, reading the value back in a `with` block uses a separate `setup` variable, not `needs.setup...` - `${setup.cluster.out.KUBECONFIG}`, no prefix inside the expression itself.

This only works for a step type that implements `Export` (`Info` reports which ones do), and only from `env` toward `setup` - never the reverse, since `setup` is provisioned independently of any `env` run. A plain `kevin run` never brings the `setup` scope up in that process, so kevin calls the setup step's `Export` RPC to resolve the value instead of walking it as a normal dependency.

## Commands

`commands` declares named, reusable actions to run against a live environment with `kevin do <name>`. A command has its own `needs` list (the same `<step>` / `setup.<name>` convention a step's `needs` uses) and a `run` argv kevin execs in place of itself, inheriting the caller's terminal:

```cue
env: web: {
    uses: "builtin:container"
    with: image: "postgres:16"
}

commands: {
    shell: {
        needs: ["web"]
        run: ["sh", "-c", `docker exec -it "${needs.web.out.name}" psql -U postgres`]
    }
}
```

```sh
kevin do shell
```

`run`'s `${...}` markers work exactly like a step's `with` block: `do` exports every step `needs` names, then renders `run` against `needs.<step>.out.<key>` (a plain entry) or `setup.<name>.out.<key>` (a `setup.`-prefixed entry) before exec'ing it - here `${needs.web.out.name}` becomes the container's real name, `builtin:container`'s own `Export` output. A command with more than one `needs` entry can read from all of them, each under its own name, unlike a single `${needs.a.out.x}` colliding with `${needs.b.out.x}` the way a flat environment-variable merge would. Extra args after `--` append to `run`: `kevin do shell -- -c "select 1"`.

Only a step whose plugin implements `Export` may appear in a command's `needs` list, and every `${needs...}`/`${setup...}` reference inside `run` must name a step `needs` actually declares; `kevin validate` checks both statically, the same way it checks a step's `with` block, so a command that can never work fails before `kevin do` ever tries it.

## Validation

Each step's `with` block is validated against a schema published by the plugin that implements it, so a misconfigured environment fails before it creates anything.

State for the unnamed environment lives in `./.kevin/`; a named environment's state lives in `./.kevin/<name>/`.
