---
title: "Environment file"
weight: 1
description: "Every field of kevin.cue: steps, plugins, commands, variables, proxy, console, and relay settings."
---

# Environment file

The environment file declares the steps of an environment, the plugins that provide them, and the settings for the proxy and console.

```cue
project: "my-app"

proxy: {
    listen:       "127.0.0.1:18080"
    gateway_port: 18082
    egress: deny: true
}
console: listen: "127.0.0.1:18081"

env: web: {
    uses: "builtin:container"
    with: image: "nginx:alpine"
}
```

## File name

| File | Environment |
|:-----|:------------|
| `kevin.cue` | The default environment. |
| `<name>.kevin.cue` | A named environment, selected with `--env <name>` or `KEVIN_ENV`. |

Each name can also start with a `.` (for example `.kevin.cue`). Exactly one file may exist for each environment name.

Two environments in one directory are independent. Each has its own project name, container network, CA, and state directory, and both can run at the same time.

### Package mode

A CUE file can start with a `package` clause. kevin then also loads every other `.cue` file in the directory with the same clause, as one environment.

- kevin ignores `.cue` files with no clause or a different clause.
- kevin never loads the file of another environment, such as `staging.kevin.cue`, into this one.
- If the environment file has no clause, but another `.cue` file in the directory has one, kevin reports an error.

`--tag`/`-t` works only in package mode.

### Tags

`--tag`/`-t` sets a field marked with a CUE `@tag` attribute:

```cue
package kevin

proxy: egress: deny: bool @tag(airgap,type=bool)
```

```sh
kevin run -t airgap
```

| Argument | Result |
|:---------|:-------|
| `-t name` | Sets the tag to `true`. |
| `-t name=value` | Sets a string, int, or number tag. |

The flag is repeatable. Without the flag, the field keeps its value from the file. See [Per-machine and per-run settings]({{< relref "/docs/guides/local-and-per-run-settings" >}}).

## Top-level fields

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `project` | `string` | directory name | Names the environment. Use lowercase letters and digits separated by single hyphens. Every resource kevin creates carries this name. A named environment appends `-<name>`, so the default is `<directory>-<name>`. |
| `domain` | `string` | `"kevin.home"` | Base domain for routes. The proxy serves `<host>.<domain>`. |
| `plugins` | `{[name]: #Plugin}` | - | Third-party plugins. See [Plugins](#plugins). |
| `setup` | `{[name]: #Step \| #StepGroup}` | - | Steps that persist across runs. `kevin setup` starts them and `kevin teardown` removes them. |
| `env` | `{[name]: #Step \| #StepGroup}` | - | Steps that `kevin run` starts and removes on exit. |
| `commands` | `{[name]: #Command}` | - | Commands that `kevin do` runs. See [Commands](#commands). |
| `variables` | `{[name]: #Variable}` | - | External inputs, read as `${vars.<name>}`. See [Variables](#variables). |
| `proxy` | `#Proxy` | - | **Required.** See [Proxy](#proxy). |
| `console` | `#Console` | - | **Required.** See [Console](#console). |
| `engine` | `#Engine` | - | See [Engine](#engine). |
| `relay` | `#Relay` | - | See [Relay](#relay). |

## Steps

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `uses` | `string` | - | **Required.** The step type, as `<plugin>:<step>`. Builtin step types use the `builtin` plugin, for example `builtin:container`. |
| `needs` | `[...string]` | `[]` | Steps that must be ready before this step starts. |
| `with` | `{...}` | - | Configuration for the step type. See [Steps]({{< relref "/docs/reference/steps" >}}) for builtin step types. |
| `label` | `string` | step name | Display name in the console. |
| `timeout` | `string` | none | Longest the step may take to start, as a Go duration such as `"2m"`. A step that takes longer fails. Must be positive. A group does not accept it. |
| `watch` | `[...string]` | `[]` | Files and directories, relative to the project directory. While `kevin run` is running, a change under any of them reruns the step and the steps that depend on it. A directory is watched recursively, and `.kevin/`, `.git/`, and editor temp files (`*~`, `.#*`, `*.swp`) are ignored. Each path must exist and stay inside the project directory. Only `env` steps accept it, and a group does not. |

Steps with no dependency between them start in parallel. If a step fails, kevin cancels steps that have not started and removes the steps that came up, and any step whose `Up` was still running, in reverse dependency order.

### Needs

A `needs` entry is one of:

| Entry | Meaning |
|:------|:--------|
| `"<step>"` | A step in the same scope. |
| `"setup.<step>"` | A `setup` step, from an `env` step. The `setup` step must already be up (`kevin setup`), and its step type must support export. A `setup` step cannot need an `env` step. |

### Reading another step's outputs

A string in `with` can contain `${...}` expressions that read the outputs of the steps in `needs`:

```cue
env: {
    cluster: {
        uses: "builtin:kubernetes"
        with: driver: "kind"
    }
    app: {
        uses:  "builtin:kubectl"
        needs: ["cluster"]
        with: {
            kubeconfig: "${needs.cluster.out.kubeconfig}"
            context:    "${needs.cluster.out.context}"
        }
    }
}
```

| Expression | Reads |
|:-----------|:------|
| `${needs.<step>.out.<key>}` | An output of a step in `needs`. |
| `${needs.<step>.system.<key>}` | A value kevin computes for a step in `needs`, such as a relay address. |
| `${setup.<step>.out.<key>}` | An output of a `setup.<step>` entry in `needs`. |
| `${env.<VAR>}` | An environment variable of the `kevin` process. |
| `${project.<key>}` | A project value, such as the CA certificate path. |
| `${vars.<name>}` | A `variables` entry's resolved value. See [Variables](#variables). |

`kevin validate` fails when an expression names a step that is not in `needs`. See [CEL expressions]({{< relref "/docs/reference/cel-expressions" >}}) for every variable and error. Each step type's reference page lists its outputs.

## Step groups

A step group has `steps` instead of `uses`. It groups steps under one name in the console, and other steps can depend on it as one unit.

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `steps` | `{[name]: #Step}` | - | **Required.** The member steps. A member's `needs` names a sibling by its bare name. |
| `needs` | `[...string]` | `[]` | Steps every member depends on, in addition to its own `needs`. |
| `outputs` | `{[key]: string}` | - | The group's outputs, as `${needs.<member>.out.<key>}` expressions over its members. |
| `label` | `string` | group name | Display name in the console. |

```cue
env: {
    db: {
        steps: {
            postgres: {
                uses: "builtin:container"
                with: {image: "postgres:16", expose: pg: {port: 5432}}
            }
            migrate: {
                uses:  "builtin:exec"
                needs: ["postgres"]
                with: up: command: ["./scripts/migrate.sh"]
            }
        }
        outputs: addr: "${needs.postgres.out.host_5432}"
    }
    app: {
        uses:  "builtin:container"
        needs: ["db"]
        with: {image: "my-app", env: DATABASE_ADDR: "${needs.db.out.addr}"}
    }
}
```

`app` starts after both `postgres` and `migrate` are ready.

A step outside the group can only need the group, not a member, and can only read the group's `outputs`.

## Commands

`kevin do <name>` runs a command against a running environment.

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `run` | `[string, ...string]` | - | **Required.** The program and its arguments. No shell: use `["sh", "-c", "..."]` for shell features. Can contain `${...}` expressions. |
| `needs` | `[...string]` | `[]` | Steps whose outputs `run` reads. Same entry forms as a step's `needs`. Each step type must support export. |
| `cwd` | `string` | project directory | Working directory. A relative path resolves against the project directory. |
| `label` | `string` | - | Display name. |

```cue
commands: psql: {
    needs: ["db"]
    run: ["sh", "-c", `docker exec -it "${needs.db.out.name}" psql -U postgres`]
}
```

```sh
kevin do psql -- -c "select 1"
```

Arguments after `--` are appended to `run`. `kevin validate` checks every command's `needs` and expressions.

## Variables

`variables` declares external inputs: values the person running `kevin run`, `setup`, or `do` supplies, rather than something computed from another step. Each key is a variable name (letters, digits, underscore, not starting with a digit - it's also a CEL identifier) mapped to:

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `type` | any CUE expression | `string` | Constrains the variable's value: a bare kind (`int`, `bool`), a bounded range (`int & >=1 & <=10`), a disjunction (`"a" \| "b"`), a struct shape, a regex (`=~"^prod-"`). If omitted, the variable is a plain string. |
| `default` | matching `type` | - | Value when nothing external supplies one, checked against `type`. Omit it to make the variable required. |
| `sensitive` | `bool` | `false` | Marks the value as secret. A field reading it via `${vars.<name>}` is always redacted in the console and MCP server, the same as a field reading an already-sensitive output. |

```cue
variables: {
    region:   {default: "us-east-1"}
    api_key:  {sensitive: true}
    replicas: {type: int & >=1 & <=10, default: 3}
}
env: app: {
    uses: "builtin:container"
    with: {
        env: {
            REGION:  "${vars.region}"
            API_KEY: "${vars.api_key}"
        }
        replicas: "${vars.replicas}"
    }
}
```

A field that reads a typed variable as its entire value (nothing else in the same string) takes on that variable's real type - a number, a bool, a list, a struct - not a string. A field that mixes a marker into surrounding text (`"prefix-${vars.x}-suffix"`) still requires the expression evaluate to a string.

A value comes from one of three sources, highest precedence first:

| Source | Example |
|:-------|:--------|
| `--var KEY=VALUE` (repeatable) | `kevin run --var api_key=sk-123` |
| `KEVIN_VAR_<NAME>` (name upper-cased) | `KEVIN_VAR_API_KEY=sk-123 kevin run` |
| `--var-file <path>` | `kevin run --var-file secrets.env`, one `KEY=VALUE` per line, blank lines and `#` comments skipped |

A plain string variable (no `type`) takes any of these three sources literally, unquoted. A typed variable parses the supplied value as CUE syntax, so `--var replicas=3` supplies the int `3` and `--var tags='["a","b"]'` supplies a list.

A key in a var-file or the environment that names no declared variable is ignored - the file can be shared across more than one `kevin.cue`. `kevin validate` fails when a required variable has no value from any source, when a value does not satisfy its declared `type`, or when a `${vars.<name>}` expression names a variable `variables` does not declare.

## Plugins

Each key of `plugins` is a plugin name that steps use in `uses: "<name>:<step>"`. A name has lowercase letters, digits, and hyphens.

These names are reserved and cannot be a `plugins` key: `builtin`, `cmd`, `core`, `docker`, `file`, `helm`, `http`, `k8s`, `kevin`, `kubectl`, `kubernetes`, `oci`, `official`, `std`.

kevin starts a plugin only when a step uses it.

### Plugin sources

Each entry sets exactly one source field.

| Source | Value | Example |
|:-------|:------|:--------|
| `cmd` | Path to a plugin binary. A relative path resolves against the project directory. | `cmd: "./bin/kevin-plugin-echo"` |
| `file` | Path to a plugin package (`.tar` or `.tar.gz`). | `file: "./dist/echo.tar.gz"` |
| `oci` | OCI reference of a plugin package, by tag or digest. | `oci: "ghcr.io/acme/kevin-plugin-echo:v1"` |
| `http` | URL of a plugin package. | `http: "https://example.com/echo.tar.gz"` |

`oci` uses the credentials from `docker login`. For a multi-architecture package, kevin selects the host's OS and architecture. kevin caches downloaded packages in `~/.kevin/pkg-cache/`.

### Plugin fields

| Field | Sources | Type | Description |
|:------|:--------|:----:|:------------|
| `config` | all | `{...}` | Configuration for the plugin, checked against the plugin's config schema and sent once before its first step. A field can read `${vars.<name>}`, `${env.<key>}`, or `${project.<key>}` (see [Variables](#variables)), not `${needs...}`/`${setup...}`. |
| `args` | all | `[...string]` | Arguments for the plugin binary. For a package, these replace the package's default arguments. |
| `env` | all | `{[string]: string}` | Environment variables for the plugin process. |
| `checksum` | `file`, `http` | `string` | SHA-256 that the package must match, as `"sha256:<hex>"`. For `oci`, pin a digest in the reference instead. Without a checksum, kevin downloads an `http` package on every run. |
| `signing` | `file`, `oci`, `http` | `#Minisign \| #Sigstore` | Requires a valid signature before kevin extracts the package. |

```cue
plugins: echo: {
    cmd:    "./bin/kevin-plugin-echo"
    config: greeting: "hello"
}
```

### Signing

| Scheme | Fields | Signature file | Trusted signers |
|:-------|:-------|:---------------|:----------------|
| `minisign` | `scheme: "minisign"` | `<package>.minisig` | `kevin plugin trust add` |
| `sigstore` | `scheme: "sigstore"`, `identity`, `issuer` | `<package>.sigstore.json` | `kevin plugin trust add-identity` |

```cue
plugins: echo: {
    oci: "ghcr.io/acme/kevin-plugin-echo:v1"
    signing: {
        scheme:   "sigstore"
        identity: "ci@acme.example"
        issuer:   "https://token.actions.githubusercontent.com"
    }
}
```

For `file` and `http`, kevin reads the signature file next to the package. For `oci`, kevin reads it from the tag `sha256-<digest>.sig` in the same repository. The `sigstore` scheme needs `cosign` installed. A package without a valid signature from a trusted signer fails, and kevin does not extract it. See [Third-party plugins]({{< relref "/docs/guides/third-party-plugins" >}}) and [Plugin trust]({{< relref "/docs/concepts/plugin-trust" >}}).

## Proxy

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `listen` | `string` | - | **Required.** Host address of the proxy, as `host:port`, for example `"127.0.0.1:18080"`. |
| `gateway_port` | `int` | - | **Required.** Port of the proxy's listener on the container network gateway. The relay connects to the proxy through it. |
| `egress.deny` | `bool` | - | **Required.** `true` blocks outbound requests to hosts that no route and no allow list covers. |
| `egress.allow` | `[...string]` | `[]` | Hosts every step can reach when `deny` is `true`. An entry is an exact host (`api.github.com`) or a wildcard (`*.github.com`). A wildcard does not match the bare domain. Matching ignores case and port. |
| `egress.passthrough` | `bool` | `false` | `true` tunnels TLS to an allowed host with no route, instead of terminating it with a kevin certificate. |

See [Proxy and egress]({{< relref "/docs/guides/proxy-and-egress" >}}).

## Console

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `listen` | `string` | - | **Required.** Host address of the web console, as `host:port`. The MCP server is at `/_mcp` on the same address. |

## Engine

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `max_parallel` | `int` | `0` | Maximum number of steps that run at the same time. `0` means no limit. |

To select docker or podman, use `--engine` or `KEVIN_ENGINE`. The environment file has no field for it.

## Relay

| Field | Type | Default | Description |
|:------|:----:|:-------:|:------------|
| `image` | `string` | release image | Relay container image. `KEVIN_RELAY_IMAGE` overrides it. |
| `intercept.ipv4_range` | `string` | `"198.18.0.0/15"` | Address range for DNS answers to intercepted hostnames. |
| `intercept.ipv6_range` | `string` | `"fd00:aaaa:bbbb::/64"` | IPv6 address range for DNS answers to intercepted hostnames. |

Change an `intercept` range only when it overlaps a network your workloads use.

## Environment variables

| Variable | Description |
|:---------|:------------|
| `KEVIN_ENV` | Default for `--env`. |
| `KEVIN_ENGINE` | Default for `--engine`: `docker` or `podman`. |
| `KEVIN_VAR_FILE` | Default for `--var-file`. |
| `KEVIN_VAR_<NAME>` | Supplies a declared variable's value, name upper-cased. See [Variables](#variables). |
| `KEVIN_PROJECT_STATE_DIR` | Project state directory. Default: `.kevin/` in the project directory, or `.kevin/<name>/` for a named environment. |
| `KEVIN_USER_STATE_DIR` | User state directory, for the root CA, trust stores, and package cache. Default: `~/.kevin/`. |
| `KEVIN_PLUGIN_CA_FILE` | PEM file of extra root certificates, trusted in addition to the system roots, for `oci:` and `http:` plugin fetches and `kevin plugin push`. Not used by `cosign`. |
| `KEVIN_RELAY_IMAGE` | Relay image. Overrides `relay.image`. |
| `KEVIN_RELAY_REPO` | Relay image repository, keeping the default tag. |
| `KEVIN_RELAY_TAG` | Relay image tag, keeping the default repository. |
| `KEVIN_RELAY_UDP_POOL_SIZE` | Number of UDP ports the relay reserves for UDP `expose` entries with `relay: true`. Default: `16`. `0` disables UDP relaying. |
