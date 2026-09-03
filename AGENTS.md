# AGENTS.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Status: pre-1.0

This project is v0 - breaking changes are acceptable and preferred over
compatibility shims. Do NOT add reserved proto fields, deprecated aliases, or
backward-compatibility layers unless explicitly asked. When a rename or
removal is requested, do the full sweep: proto, Go code, mocks, tests, docs,
and examples.

## What this is

kevin: local dev environment supervisor. Brings up a Docker-backed test
environment from a DAG of steps (`kevin.cue`), exposes a web console, an
MCP server for coding agents (mounted on the console's own listener at
`/_mcp`), and an HTTP/HTTPS proxy with TLS termination and default-deny
egress, tears down on exit. Every step type is a plugin speaking gRPC -
`container`, `exec`, `kind`, `kubectl`, `helm`, `wait`, `route` ship as
builtins inside the `kevin` binary; third-party plugins are separate binaries. Full
design
rationale:
[docs/site/content/docs/concepts/architecture.md](docs/site/content/docs/concepts/architecture.md)
- read it before touching the DAG engine, the proxy, the CA, or the plugin
protocol; it explains *why*, not just *what*.

## Scope Control

Implement exactly what was asked. Do not add adjacent features (e.g. UDP
support, extra validation helpers) or run destructive housekeeping commands
like `go mod tidy` without asking first. If extra work seems warranted,
propose it in one sentence and wait.

## Build system: gnob

No Makefile. Build orchestration is `gnob` (github.com/justenwalker/gnob),
vendored under `build/` as plain Go - bootstrap once, then it self-rebuilds
when its own sources change.

```sh
go generate -C ./build -tags gnob .            # bootstrap, once
./build/gnob -help                             # list every target, with a one-line description each
./build/gnob                                   # default: build into bin/
```

`generate` regenerates `protos/pb` (buf), the templ components, and each
builtin plugin's reference doc page from its `schema.cue`. `test` runs
`go test -race -cover ./...`; `integration` runs `go test -tags
integration ./...` and needs Docker.

Tool versions (buf, templ, golangci-lint, gofumpt, gci, protoc plugins,
goreleaser) are pinned in `tools.mod`/`tools.sum` via Go's tool directive,
not installed globally.

```sh
GITHUB_TOKEN=$(gh auth token) ./build/gnob release vX.Y.Z
```

`release` needs `GITHUB_TOKEN` (repo + `write:packages` scope) exported,
`docker login ghcr.io` already done, and the active buildx builder on a
`docker-container` driver (or the containerd image store enabled) - the
`kevin-relay` image build emits an SBOM attestation, which the plain
`docker` driver can't produce. Set up once with
`docker buildx create --name relay-builder --driver docker-container
--bootstrap --use` (or `docker buildx use <existing docker-container
builder>` if one already exists idle; or enable containerd image storage
in Docker Desktop/OrbStack's settings instead, which lets the default
`docker` driver do it too). gnob does none of this for you. It writes and
commits `internal/version/VERSION` (embedded by `internal/version`, which
`internal/cmd` reads for `kevin --version` and `internal/relay` reads to
build the default relay image tag - `ghcr.io/justenwalker/kevin/relay:` +
that version, or the locally-built `kevin-relay:dev` while still
unreleased - so both read correctly even from a `go install`/module-proxy
build, not just a goreleaser one) before tagging, locally only. It then
does a full local `goreleaser release --clean --skip=publish`
build (config: `.goreleaser.yaml`, which also sets `-trimpath` and a
commit-derived `mod_timestamp` for reproducible builds) to catch build
failures with no network writes at all. Only once that succeeds does it
`git push origin HEAD` - the commit only, no tag: GitHub's release API
can't attach a release to a commit it's never received, so it has to land
on the remote before that call, but the tag itself should still only
exist once the release actually succeeds. `release.target_commitish` in
`.goreleaser.yaml` is set to `{{ .FullCommit }}` so GoReleaser creates the
real `vX.Y.Z` tag against that exact commit as part of publishing -
GoReleaser pushes the `kevin-relay` image before creating the GitHub
release and aborts on the first failure, so a docker push failure still
leaves no tag or GitHub release behind.

## Commit Discipline

- Before starting new work, confirm the working tree is clean and you are on
  the correct base branch (ask if unsure). Never branch off `main` when a
  feature branch is the intended base.
- Stage files explicitly by path. Never use `git add .` or `git add -A` -
  unrelated staged deletions have been swept into commits before.
- Group work into small, logical commits as you go rather than one
  monolithic commit at the end. Each commit must build, test, and lint clean.
- Write real commit messages: subject describes the behavior change, body
  explains why.

## Testing

- Unit tests: `go test -race -cover ./...` (what `gnob test` runs).
- A single package/test: `go test ./internal/dag/... -run TestName -v`.
- Integration tests are gated behind the `integration` build tag (see files
  named `integration_test.go` in `cmd/kevin-relay`, `internal/plugins/kind`,
  `internal/plugins/container`, `internal/relay`, `internal/engine`) and
  generally require Docker. Run with `go test -tags integration ./...`.
- Try a real environment end-to-end:
  ```sh
  ./build/gnob build
  ./bin/kevin -C examples/web run      # Ctrl-C to remove
  ```
  Other example environments: `examples/echo` (provider with no real
  resource, demonstrates DAG fan-out/fan-in and failure propagation),
  `examples/kind` (Kubernetes cluster), `examples/intercept` (a `route`
  step's `external: true` fakes out a real-world hostname with a local
  container). `kevin ca install`/`uninstall` manages the CA trust store;
  it needs no project (see the quickstart's "Trust the CA" section).

## Definition of Done

A task is not complete until all of the following hold:

1. `go build ./...`, `go vet ./...`, `go test -race -cover ./...`, and
   `./build/gnob lint` all pass.
2. Docs affected by the change are updated in the same commit, for any
   user-facing behavior change: architecture/concepts pages, the feature
   index, comparison page, README, and generated reference docs.
3. A fresh subagent with no implementation context reviews the diff alone,
   with nothing beyond the diff itself as input. Ask it to find over-scoped
   changes, unnecessary comments, violations of AGENTS.md/GO_CONVENTIONS.md,
   and missing test coverage. Resolve each finding, or note why it doesn't
   apply, before moving on.
4. Work is committed as it goes, split by logical concern, never as one
   monolithic commit at the end - see Commit Discipline above.
5. Report to the user what the reviewer subagent flagged and how each item
   was resolved (fixed, or why not), before calling the task done.

## Linting

`golangci-lint` (v2 config, `.golangci.yaml`) enables *all* linters and
disables specific ones deliberately - the yaml file's comments explain each
exclusion's reasoning (e.g. sentinel errors as `type Error string` are house
style, not an `err113` violation; `(nil, nil)` is a legitimate lookup-miss
result; short names are fine in short scopes). Read those comments before
adding a `//nolint` - the project likely already made the call. Generated
code (`protos/pb/`, `*_templ.go`) and `_test.go` files have their own
exclusion rules.

[docs/GO_CONVENTIONS.md](docs/GO_CONVENTIONS.md) covers the house style
`golangci-lint` can't check - sentinel errors, error-message shape, helper
placement, and the rest, each as a numbered rule (`GO-###`) with a DO/DO NOT
example, including [GO-019](docs/GO_CONVENTIONS.md#go-019-a-function-returns-a-single-struct-plus-error-not-three-or-more-values)
on Go API shape.

## Writing Style

- No em-dashes anywhere. Keep headings short.
- Do not add explanatory comments for self-evident code. Comments explain
  *why*, never *what* - see [docs/GO_CONVENTIONS.md](docs/GO_CONVENTIONS.md)
  GO-006 for the doc-comment vs. inline-comment split.
- Avoid AI-flavored filler ("comprehensive", "robust", "seamlessly",
  "leverage"). Write plain user-facing language, not marketing jargon.
- Follow [docs/GO_CONVENTIONS.md](docs/GO_CONVENTIONS.md) for test file
  naming and test function names; keep test names short and match existing
  files rather than creating new one-off scenario files.

## Architecture

Key model to hold in your head when changing any of this:

- **Provider model**: a plugin process is a *provider* that offers one or
  more step types. A step's `uses: "<plugin>:<step>"` names both parts.
  `builtin` (never declared in `plugins:`) offers `container`, `exec`,
  `kind`, `kubectl`, `helm`, `wait`, `route`. One process serves every step
  type it offers and must be safe for concurrent `Up`/`Down` calls - the DAG
  can create several steps of the same type at once.
- **Two independent DAG scopes** sharing one engine/protocol: `setup`
  (persists across runs, `kevin setup`/`teardown`) and `env` (ephemeral,
  `kevin run`). State lives under `./.kevin/` (or `./.kevin/<name>/` for a
  named environment selected with `-e`/`--env`), keyed by project name so
  two projects, or two named environments in the same project directory,
  can run concurrently.
- **gRPC protocol has six RPCs**: `Info` (provider/version + CUE schemas),
  `Configure`, `Up`, `Down`, `Export`, `CallTool` (invokes a plugin-exposed
  MCP tool). `Up`/`Down` are server-streaming -
  logs, progress, and the final result all go over the same call, so there's
  no separate progress service and no `GRPCBroker`/callback path. Everything
  a plugin needs (docker network name, CA cert, proxy address, workspace
  path, upstream outputs) is in the request.
- **Session bring-up order**: unify `kevin.cue` with core schema → start
  declared plugins → `Info` from each → unify each step's `with` block
  against its plugin's schema → `Configure` each plugin once → walk the DAG
  calling `Up`. A malformed environment fails at schema-unify, before
  anything is created.
- **Cross-step values**: a step's `Result.Outputs` are handed to every step
  with a `needs` edge on it (e.g. a registry endpoint, a kubeconfig path).
  An `env` step's `needs` may additionally name a `setup` step as
  `setup.<name>` (one-way only) - resolved via that step's `Export` RPC
  instead of `Up`, since a plain `kevin run` never brings the setup scope
  up. `${setup.<name>.out.<key>}` reads it back, a CEL variable separate
  from `needs` (not `needs.setup...`) so a same-scope step literally named
  `setup` stays unambiguous.
- **No state file, anywhere.** Docker resources carry `kevin.project`/
  `kevin.step` labels; `Down` must be derived from live state and be
  idempotent (survives a crash mid-run). Same principle for CA
  (`LoadOrGenerateIntermediate` re-derives/replaces a stale intermediate) and
  for the trust store (`kevin ca uninstall` matches on the root's constant
  `CommonName`, not a saved list - it lives outside the DAG/setup scope
  entirely, since the root names no project).
- **The proxy runs on the host process**, not in a container - so it can't
  resolve a container network alias and (on macOS) can't reach a container
  address directly. A route must be something the host can dial; the
  container plugin publishes a step's port on loopback and returns that as
  the upstream. A step is "ready" when its published port accepts a
  connection, not when the container reports `Running`.
- **Egress is default-deny.** `proxy: egress: allow` in `kevin.cue` is
  environment-wide; a step's `Up` result can add hosts for itself via
  `egress_allow`. Denied requests still complete the TLS MITM and get a
  `403` naming the host and the CUE fix, with cache-busting headers.
- **Reserved plugin namespaces** (`builtin`, `cmd`, `core`, `docker`, `file`,
  `helm`, `http`, `k8s`, `kevin`, `kubectl`, `kubernetes`, `oci`, `official`,
  `std`) can't be used as a `plugins:` key - keeps a third-party plugin from
  reading as first-party.
- **Docker via CLI, not SDK.** kevin shells out to `docker` and parses JSON
  output rather than importing `github.com/docker/docker`, to avoid that
  dependency tree. Kubernetes support is a plugin (`internal/plugins/kind`)
  that shells out to the host `kind` binary the same way `kubectl` and
  `helm` shell out to theirs, rather than importing `sigs.k8s.io/kind` as a
  library - that library reads proxy variables from its own process
  environment with no way to pass them in otherwise, which shelling out
  avoids: `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` are scoped to each `kind`
  child process's own `exec.Cmd.Env`, not kevin's.
  OCI registry access (`internal/ocipkg`) is the exception that proves the
  rule: it imports `cuelabs.dev/go/oci/ociregistry` rather than shelling out
  to `docker pull`, but that costs no new dependency tree - the module is
  already resolved transitively via `cuelang.org/go`'s own OCI-registry
  support - and a registry pull is a plain HTTP API, not a
  container-runtime SDK.

## Environment files (`kevin.cue`)

No package clause. `project`, `env` (map of steps), optional `plugins` (map
keyed by plugin name, entries have `cmd`, `file`, `oci`, or `http` - the
sources kevin implements; `file` names a tar package
(`internal/pluginpkg`), `oci` names the same package fetched from an OCI
registry (`internal/ocipkg`), `http` names the same package fetched over a
plain URL (`internal/httppkg`) - `oci` and `http` share one
content-addressed cache (`internal/pkgcache`) - all extracted into the
project workspace and launched exactly like `cmd`. A `file`/`oci`/`http`
entry can also set `signed: true` (never `cmd`, which is a local binary
kevin already trusts by construction): kevin then requires a detached
minisign signature alongside the package - a sibling `.minisig` file for
`file`/`http`, or an OCI artifact at a cosign-style fallback tag for `oci`
- verified against the local trust store (`~/.kevin/trusted-keys`,
`internal/pkgtrust`), managed with `kevin plugin trust
add`/`list`/`remove`. `kevin plugin pack` builds a package from a
directory and `kevin plugin push` publishes one (plus its `.minisig`
sibling, if present) to an OCI registry. An optional `config` block is
delivered once via `Configure`). See
[docs/site/content/docs/environment-file.md](docs/site/content/docs/environment-file.md)
for worked examples.
