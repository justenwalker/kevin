# Changelog

All notable changes to kevin are listed here, newest first. kevin is
pre-1.0, so any release may contain breaking changes; they are marked
under Breaking.

## v0.0.13 (2026-10-02)

### Added

- `builtin:kubernetes` step with `kind`, `k3d`, and `minikube` drivers.
- `k3d` driver on podman; `disable`, `env`, `memory`, `labels`, and node `mounts` for k3d.
- Relay ports are published into Kubernetes clusters through a forwarder container.
- Typed `variables:` with CUE-native constraints, substituted into plugin `config:` blocks.
- A denied-host list in the error when a step fails to start.

### Changed

- Plugins exit when the supervising kevin process disappears.
- Container network egress routes through the joined network.

### Fixed

- A step still in `Up` at cancellation now gets `Down` called.
- k3d node addresses are kept out of the proxy.
- The relay's DNS servers stop when the context ends before they start.
## v0.0.12 (2026-09-27)

### Breaking

- Environment files are CUE only; YAML and JSON are removed.

### Added

- `variables:` block, read as `${vars.<name>}`, with `--var` and `--var-file` flags.
- `@sensitive()` attribute redacts a `with` field in inputs and outputs.
- Console: per-step detail dialog for inputs and outputs, and a banner when the SSE connection drops.
- MCP `get_step` reports a step's inputs and outputs.
- Federated plugin discovery, index, and install.
- Terminal UI collapses pending, ready, skipped, and removed steps into a summary line.
- Docs rewritten by page type (tutorial, guides, reference, concepts).

### Fixed

- Console: dependency lines no longer double back on close steps.
## v0.0.11 (2026-09-21)

### Added

- UDP support for relay-routed exposed ports.
- Sigstore keyless signing for plugin packages.
- `kevin status` and `kevin logs`, and a cheaper MCP `get_step`.
- `fault`: `rate_kbit` bandwidth cap.
- `examples/n8n-aws`.

### Fixed

- The Docker network and relay are cleaned up when startup fails.
## v0.0.10 (2026-09-15)

### Added

- `builtin:fault` step for fault injection.
- `kevin doctor` preflight check.
- `kevin run --detach` and `kevin stop`.
- Podman as a second container engine, selected with `--engine` or `KEVIN_ENGINE`; kind nodes run on the selected engine.
- `kind`: per-node config passthrough via `control_plane` and `workers`, and named worker nodes.
- MCP `rerun_step` returns full step detail and proxy denials.
- `kevin ca` reports trust status read-only.

### Fixed

- `kind` nodes are no longer dual-homed on kind's default network.
## v0.0.9 (2026-09-07)

### Added

- `proxy.egress.passthrough` skips MITM for unrouted egress.
- A `maxParallel` cap on concurrent steps in the DAG.
- Console redesign: step cards, rerun controls, dependency lines, tabs, header, group styling, cross-scope need badges, and an empty Services tab placeholder.

### Fixed

- Console: a running step's progress bar updates in place.
- Intercept and wildcard routes are not linked on their card.
## v0.0.8 (2026-09-05)

### Breaking

- The route field `external` is renamed `intercept`.
- Route `tls`, `skip_mitm`, and `raw` booleans are replaced by one route mode.

### Added

- Transparent capture of container egress via nftables in the relay.
- Fake-IP dispatch for registered intercepts, with configurable address pools (ULA default for IPv6).
- Dual-stack (IPv4/IPv6) relay networking.
- The relay control channel is gRPC over mutual TLS.
- `kind` nodes register for transparent egress capture.

### Fixed

- A route claiming passthrough without TLS falls back to MITM.
## v0.0.7 (2026-09-04)

### Breaking

- `proxy.egress.deny` has no schema default and must be set.

### Added

- Step groups.
- `route`: `skip_mitm` opts a TLS route out of MITM.
- Intercept routes reach a pod's own DNS, and `kind` node DNS points at the relay.
- `project.relay` exposes the relay's address.
- `kevin setup --open`.
- `examples/showcase`.

### Fixed

- Two relay routes no longer share one keep-alive connection.
- A step's output is recorded when it completes, not after the whole walk.
## v0.0.6 (2026-09-04)

### Breaking

- `proxy.listen`, `gateway_port`, and `console.listen` must be set explicitly.

### Added

- CUE package-mode loading and `@tag` mode switches.

### Fixed

- `kind` cluster reuse accounts for the proxy address.
## v0.0.5 (2026-09-04)

### Breaking

- `expose` on `kind` and `container` is a name-keyed map, not a list.

### Added

- `ExposedPort.host_port` pins a step's local forward.
- `kind`: `host_port` and resolved `extra_mounts.host_path`.
- `exec` implements `Export`, so it can move to setup scope.
- `project.dir` in the project CEL scope.
## v0.0.4 (2026-09-03)

### Breaking

- `Export` reports structured `Out` only; `Env` is removed.
- `kevin connect` is replaced by `kevin do`.

### Added

- `commands:` block and `kevin do`, replacing `kevin connect`.
- Static validation of `needs.<step>` and `setup.<name>` references.
- `container` implements `Export` (id, name, ip).
- `kind`: `extra_mounts`.
- Architecture decision records.
- Hyphenated step and plugin names.

### Fixed

- A `tls: true` upstream is verified by its real name against the kevin CA.
- `kind` reuses a persistent cluster instead of recreating it on `Up`.
## v0.0.3 (2026-09-03)

### Added

- `builtin:exec` step.
- `CallTool`, a sixth plugin RPC for plugin-exposed MCP tools.
- `project.*` CEL scope for project-level constants.
- Optional `kevin.local.cue` override file.
- `container` expose entries can route through the relay's SOCKS5 gateway.

### Fixed

- The `with` block is rendered before `Export`.
## v0.0.2 (2026-09-01)

### Breaking

- The relay always runs; the `enabled` toggle is removed.

### Added

- Release checksums are signed with minisign.
- `kevin ca install` and `kevin ca uninstall`, replacing `builtin:trust`.
- A command reference page per top-level command.
- `examples/s3-app`.

### Changed

- Setup and env scopes coexist safely across process boundaries; `setup.<name>` needs resolve through `Export`.
## v0.0.1 (2026-08-29)

Initial release.
