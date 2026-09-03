# Manual end-to-end test plan

A checklist for exercising every kevin feature by hand, against a real Docker
daemon. Run top to bottom on a release candidate, or pick a section after a
change to the area it covers. Each step lists a command and the expected
result; check it off only after you've actually seen that result, not just
that the command exited zero.

Prerequisites: Docker daemon running, this repo cloned, nothing else bound to
ports 18080-18081.

```sh
go generate -C ./build -tags gnob .
./build/gnob build
export PATH="$PWD/bin:$PATH"
```

`kevin` and `kevin-plugin-echo` should now be on `PATH` from `bin/`.
`kevin-relay` never lands there - it only builds as a Docker image
(`./build/gnob relay-image`, tagged `kevin-relay:dev`), used inside
containers, never as a host binary.

Before starting, clear any leftover state from previous manual runs so a
stale workspace doesn't mask a real bug:

```sh
git status examples   # every examples/*/.kevin should be untracked/ignored
rm -rf examples/*/.kevin
```

## 1. `kevin run` - basic env lifecycle

_Automated by `gnob e2e` (`tests/e2e/lifecycle_test.go`)._

```sh
kevin -C examples/web run
```

- [ ] Prints `console` and `proxy` addresses, and the `export HTTP_PROXY=...`
      hint, once both are listening.
- [ ] In an actual terminal (not piped), draws a live redrawing list: one row
      per step, its state, a progress bar once an estimate exists.
- [ ] `web`, `web_route`, `probe`, `noproxy` all reach `Ready`.
- [ ] `.kevin/kevin.log` exists and contains full JSON lines, including
      debug-level ones, for this run.
- [ ] `Ctrl-C` tears down `probe`, `noproxy`, `web_route`, `web` in reverse
      order, and the containers are gone (`docker ps -a` shows none labeled
      `kevin.project=web-example`).
- [ ] `kevin -C examples/web run --keep`, then `Ctrl-C`: containers are left
      running this time. Confirm with `docker ps`, then manually clean up
      (`docker rm -f` the labeled containers, or a second plain `run` will
      conflict with them).

Piped/non-terminal output:

```sh
kevin -C examples/web run | cat
```

- [ ] Falls back to a plain line-per-event stream instead of the redrawing
      list.

```sh
kevin -C examples/web --debug run
```

- [ ] Falls back to the plain stream even in a terminal, and lines are at
      debug level.

## 2. Proxy - TLS termination, routing, `NO_PROXY`

_Automated by `gnob e2e` (`tests/e2e/proxy_test.go`)._

With `examples/web` up (`kevin -C examples/web run`, separate terminal):

```sh
curl --proxy http://127.0.0.1:18080 --cacert examples/web/.kevin/root.crt https://web.kevin.home/
```

- [ ] Returns the nginx welcome page. Certificate is signed by the project's
      own CA (`examples/web/.kevin/root.crt`), not a real one.

```sh
curl http://127.0.0.1:18080/proxy.pac
```

(No `--proxy` here - that would route the request *through* the proxy as a
forward-proxy request for host `127.0.0.1`, which egress-denies with a 403,
instead of hitting the proxy's own endpoint directly.)

- [ ] Returns a PAC file that sends `*.kevin.home` through the proxy and
      everything else `DIRECT`.

- [ ] Point an actual browser at `http://127.0.0.1:18080/proxy.pac` as its
      auto-config URL, then visit `https://web.kevin.home/` - loads the page
      (after accepting or installing the cert, see section 4), and a normal
      site (e.g. `https://example.com/`) still loads directly, unaffected.

- [ ] `docker logs <noproxy container>` shows the same fetched page as
      `probe`'s logs - `noproxy` sets `proxy: false` and still reaches `web`
      by step name over the docker network, without any proxy env vars.

Add a second entry to `web_route`'s `routes` list:
`{host: "*.web", address: "${needs.web.out.host_80}"}` - no `external: true`.

- [ ] `curl --proxy http://127.0.0.1:18080 --cacert examples/web/.kevin/root.crt https://anything.web.kevin.home/`
      also reaches the nginx page - a host wildcard matches a subdomain
      the same way with or without `external: true`.
- [ ] The same request against the bare `web.kevin.home` (no subdomain) is
      unaffected by the wildcard entry - it still only matches through the
      plain `web` entry already there, not the `*.web` one.

## 3. Egress control

_Automated by `gnob e2e` (`tests/e2e/proxy_test.go`)._

```sh
curl --proxy http://127.0.0.1:18080 --cacert examples/web/.kevin/root.crt https://example.com/
```

- [ ] Returns `403`, page names `example.com` and the exact CUE
      (`proxy: egress: allow: ["example.com"]`) to add.
- [ ] Response carries cache-busting headers (`Cache-Control`, etc. -
      inspect with `curl -i`).

Edit `examples/web/kevin.cue` temporarily, add:

```cue
proxy: egress: allow: ["example.com"]
```

- [ ] Restart `kevin -C examples/web run`; the same `curl` to
      `https://example.com/` now succeeds.

Revert the edit. Then temporarily add `proxy: egress: deny: false` at the top
level.

- [ ] Restart; every external host is now reachable through the proxy with no
      403 at all.

Revert the edit before moving on.

## 4. CA and trust store (`kevin ca`)

```sh
kevin ca install
```

- [ ] Works with no `kevin.cue` in the cwd - no project needed.
- [ ] Installs into the user's trust store (no root needed); on macOS,
      prompts for confirmation of the trust settings change.
- [ ] If `certutil` (nss) is present, also installs into Firefox's own DB; if
      absent, prints a skip for Firefox rather than failing.

```sh
kevin ca uninstall
```

- [ ] Removes what install added; safe to run again immediately
      (idempotent - run it twice in a row, second run is a no-op, not an
      error).

Combine with `examples/web`: run `kevin ca install`, then `kevin -C
examples/web run`, then hit `https://web.kevin.home/` with a plain `curl
--proxy http://127.0.0.1:18080` (no `--cacert`).

- [ ] Succeeds with no cert flag, since the root is now trusted machine-wide.
      `kevin ca uninstall` afterward removes it again.

## 5. Console

_Automated by `gnob e2e` (`tests/e2e/console_test.go`), the HTTP-checkable
parts only - `--open`'s actual browser launch and pointing a real browser at
the PAC URL stay manual._

With any environment up (`examples/web` is enough):

- [ ] Open `http://127.0.0.1:18081/` (or whatever the run printed) - shows
      the step DAG.
- [ ] Step cards show state and `label` text (`"Web Server"`, not `web`).
- [ ] Logs for each step are visible and update live as the step runs.
- [ ] Traffic through the proxy (the `curl` calls from section 2) shows up in
      the console's traffic view.
- [ ] `kevin -C examples/web run --open` launches this page in the default
      browser automatically.
- [ ] Trigger a step's rerun from the console UI - step transitions back
      through its lifecycle and reaches `Ready` again.

## 6. DAG ordering and failure propagation (`examples/echo`)

_Automated by `gnob e2e` (`tests/e2e/dag_test.go`)._

```sh
kevin -C examples/echo run
```

- [ ] `a` starts first; `b` and `c` start together only after `a` is `Ready`;
      `d` waits for both `b` and `c`.
- [ ] `hold` (`builtin:wait`, `duration: "10s"`) keeps the env up for ~10s
      after `d`.
- [ ] `boom` (`echo:fail`) always fails.
- [ ] `e` (needs `boom`) never starts.
- [ ] On `boom`'s failure, kevin cancels `e`, then removes `hold`, `d`, `c`,
      `b`, `a` in reverse order.
- [ ] Process exits non-zero.
- [ ] Provider config delivery: `a`'s output includes `greeting: "hi"`
      (step-level `outputs`), and provider-level `config.greeting` (`"hello
      from the provider config"`, set once via `Configure`) shows up wherever
      `kevin-plugin-echo` logs/echoes it.

## 7. `builtin:kind`, `builtin:kubectl`, `builtin:helm`, relay routing

_Automated by `gnob e2e` (`tests/e2e/kind_test.go`)._

Needs Docker; kind pulls node images the first time, so this is slower.

```sh
kevin -C examples/kind run
```

- [ ] `registry` comes up, `registry_ready` (`builtin:wait`, plain HTTP
      check) passes before `cluster` needs it.
- [ ] `cluster` (`builtin:kind`) creates a real kind cluster; cluster nodes
      join both the kind network and kevin's shared network.
- [ ] `KUBECONFIG=examples/kind/.kevin/kubeconfig/kind-example-cluster kubectl get nodes`
      from the host shows the node(s) `Ready`.
- [ ] `apiserver_ready` (`builtin:wait`, `tcp` check through the cluster's
      SOCKS5 relay) passes.
- [ ] `app` (`builtin:kubectl`) applies the inline Deployment+Service
      manifest; `app_ready` (`builtin:wait`, `rollout: true`) gates on it.
- [ ] `chart` (`builtin:helm`, `wait: ""`) installs the local `charts/hello`
      chart with helm's own wait disabled; `chart_ready` (`builtin:wait`,
      `for: "condition=Available"`) gates on it instead.
- [ ] `app_route` (`builtin:route`, `relay: ...`) puts `app.kevin.home` on
      the domain via the relay.
- [ ] `curl --proxy http://127.0.0.1:18080 --cacert examples/kind/.kevin/root.crt https://app.kevin.home/`
      reaches the nginx pod through the relay-routed Service.
- [ ] A pod in the cluster can pull `nginx:alpine`/other public images
      through the proxy (cluster's own `egress: ["docker.io", ...]` opens
      that without touching the environment-wide allow list).
- [ ] `Ctrl-C` on `run` removes the cluster and containers. (No `setup`
      steps remain in this example - `kevin ca uninstall` manages CA trust
      separately, see section 4.)

Add `extra_mounts: [{host_path: "/tmp/some-dir", container_path: "/host-src"}]`
to `cluster`'s `with` block, alongside `relay: true` or an `expose` entry:

- [ ] `docker exec <cluster>-control-plane ls /host-src` shows the host
      directory's contents - a bind mount into the node, generated
      alongside the relay's `extraPortMappings` in the same config, not a
      replacement for it.
- [ ] Setting `config:` (a raw kind config) at the same time makes
      `extra_mounts` a no-op, the same way it already does for `workers` -
      write the mount into the raw config yourself instead.

Put `cluster` in `setup` scope instead, and add an `env` step needing
`setup.cluster` that applies a manifest with `keep: true`:

- [ ] `kevin setup`, then `kevin run`, then `Ctrl-C`: the `keep: true`
      step's `Down` still runs (it logs removing/keeping, same as any
      other step), but the manifest it applied is still there afterward -
      `kubectl get` against the still-live `setup` cluster shows it.
      `helm`'s `keep` field behaves the same way for a release.
- [ ] `kevin setup` a second time, with `cluster`'s `with` block
      unchanged: logs `reusing cluster` rather than `creating cluster`,
      returns in a few seconds instead of the minute or so real cluster
      creation costs, and the manifest `keep: true` applied earlier is
      still there - the cluster itself was never destroyed.
- [ ] Change `cluster`'s `with` block (add a worker, say), then
      `kevin setup` again: this time it does recreate - `docker ps -a`
      shows a new control-plane container, and the manifest is gone.
- [ ] `kevin teardown` afterward removes the cluster (and the manifest
      with it).

## 8. `builtin:route` with `external: true` (`examples/intercept`)

_Automated by `gnob e2e` (`tests/e2e/intercept_test.go`)._

```sh
kevin -C examples/intercept run
```

- [ ] `fake_s3` (MiniStack) comes up; `fake_s3_ready` waits for a real HTTP
      response, not just the TCP port.
- [ ] `s3_intercept` registers `s3.us-east-1.amazonaws.com` and
      `*.s3.us-east-1.amazonaws.com` as `external: true` routes - a real
      internet hostname, not a `<step>.kevin.home` name.
- [ ] `probe` runs unmodified `aws-cli` (no `--endpoint-url`) against those
      real hostnames and it lands on `fake_s3`: `docker logs` on the probe
      container shows `mb`, `cp`, `ls`, and the read-back all succeeding.
- [ ] From the host: `curl --proxy http://127.0.0.1:18080 --cacert examples/intercept/.kevin/root.crt https://s3.us-east-1.amazonaws.com/`
      also lands on the fake, confirming the interception isn't
      container-only.

## 9. `kevin do`

_Automated by `gnob e2e` (`tests/e2e/kind_test.go`, `tests/e2e/do_test.go`,
`tests/e2e/env_test.go`)._

With `examples/kind` up in another terminal:

```sh
kevin -C examples/kind do nodes
```

- [ ] Runs `kubectl get nodes` with `--kubeconfig` rendered from
      `${needs.cluster.out.kubeconfig}` - the command's own defined argv,
      no shell, no env var to set by hand.

```sh
kevin -C examples/kind do nodes -- -o wide
```

- [ ] Extra args after `--` append to the command's `run` argv.

```sh
kevin -C examples/kind do nope
```

- [ ] No command named `nope` - errors listing the available command names,
      cleanly (not a crash).

`kevin validate` rejects a `commands:` entry whose `needs` names a step
that doesn't implement `Export`, or whose `run` references a step `needs`
doesn't declare, before `kevin do` (or Docker) ever runs:

```sh
kevin -C examples/web validate
```

- [ ] Add a `commands:` entry with `needs: ["web"]` to a copy of
      `examples/web/kevin.cue` (the `container` step type implements
      `Export`) but a `commands:` entry needing a step type that doesn't
      (e.g. `builtin:exec`) fails validate, naming the step and "does not
      implement export".

`do` renders `run`'s `${needs...}`/`${setup...}` markers the same way a
step's `with` block renders - not just against a command's own `needs`
steps, but each needed step's own `with` block first gets
`${env...}`/`${project...}`/`${setup.<name>.out.<key>}` rendered too, even
with no `kevin setup` ever having run first (`Export` is side-effect-free).
Add a step with `with: registry: "${env.REGISTRY_HOST}"` (or
`${project.root_cert}`, or a `setup:`/`env:` pair using
`${setup.<name>.out.<key>}`) to a step that supports `Export`, and a
`commands:` entry whose `run` reads it back via
`${needs.<step>.out.registry}`:

- [ ] `REGISTRY_HOST=foo kevin do <name>` prints the real value, not the
      literal `${env.REGISTRY_HOST}` template.
- [ ] Same for `${project.root_cert}` - prints the real CA path.
- [ ] Same for `${setup.<name>.out.<key>}`, with no `kevin setup` run
      first.

## 10. `kevin validate` / `kevin init`

_Automated by `gnob e2e` (`tests/e2e/cli_test.go`)._

```sh
kevin -C examples/kind validate
```

- [ ] Needs no Docker daemon running (stop Docker Desktop / OrbStack to
      confirm, then restart it) - unifies schemas and reports
      `<project>: N setup step(s), M env step(s)`, creates nothing.

```sh
kevin -C examples/web validate
```

Now break it - edit `examples/web/kevin.cue`, set `image` to a number instead
of a string.

- [ ] `validate` fails at schema-unify with a clear CUE error, before
      touching Docker. Revert the edit.

```sh
kevin -C examples/echo init
```

- [ ] `init` prints the plugin name (`echo`) either way - it lists every
      non-builtin plugin a step uses, `cmd:`-sourced or not. Since
      `echo:echo`/`echo:fail` are a local `cmd:` binary
      (`../../bin/kevin-plugin-echo`), not `file`/`oci`/`http`, nothing is
      downloaded and no process starts.

## 11. Named environments

_Automated by `gnob e2e` (`tests/e2e/env_test.go`)._

In a scratch directory:

```sh
mkdir -p /tmp/kevin-named && cd /tmp/kevin-named
cp /path/to/repo/examples/echo/kevin.cue staging.kevin.cue
kevin --env staging run
```

- [ ] Picks up `staging.kevin.cue` instead of looking for a plain
      `kevin.cue`.
- [ ] Default `project` becomes `<dirname>-staging` (check the console title
      or `docker ps` label `kevin.project`).
- [ ] State lands under `./.kevin/staging/`, not `./.kevin/`.
- [ ] With `KEVIN_ENV=staging` exported instead of `--env staging`, same
      result with no flag.
- [ ] Copy a second file as plain `kevin.cue` alongside `staging.kevin.cue`
      and run both (`kevin run` and `kevin --env staging run`,
      simultaneously, two terminals) - independent Docker networks, CA
      state, and workspaces; both come up without colliding.

## 12. Cross-step values / CEL expressions

_Automated by `gnob e2e` (`tests/e2e/env_test.go`)._

Already exercised structurally in sections 6-8 (`${needs.cluster.out.kubeconfig}`,
`${needs.web.out.host_80}`, `${needs.cluster.system.expose_apiserver}`). Additionally:

- [ ] Add a step with `with: registry: "${env.REGISTRY_HOST}"` and run
      without `REGISTRY_HOST` set - fails with a clear "variable not set"
      error, not a panic or a silently empty string.
- [ ] Set `REGISTRY_HOST` and rerun - the value is spliced in correctly.
- [ ] `${has(env.REGISTRY_HOST) ? env.REGISTRY_HOST : "localhost:5000"}` with
      the var unset falls back to `localhost:5000` instead of erroring.
- [ ] An `env` step's `needs: ["setup.<name>"]` reads a `setup` step's
      `Export` output via `${setup.<name>.out.<key>}`, resolved correctly
      even when `kevin setup` already ran and exited in a wholly separate
      process before `kevin run` starts (`tests/e2e/env_test.go`'s
      `TestCrossScopeNeedsSurvivesSeparateProcesses`). A value the setup
      step's `Export` marks sensitive keeps that flag crossing scopes and
      processes, into the receiving step's own `Deps`.
- [ ] `needs: ["setup.missing"]` (unknown setup step), `needs: ["missing"]`
      (unknown same-scope step), and a `setup`-scope step naming
      `needs: ["setup.x"]` (the prefix used outside the `env` scope) each
      fail `kevin run`/`kevin setup` with a clear error before any step
      runs, not a generic "unknown step" message. (Automated at the unit
      level, not `gnob e2e` - `internal/engine/engine_test.go`'s
      `TestRunCrossScopeNeeds`.)
- [ ] `with: message: "${project.root_cert}"` splices in the real host
      path of kevin's root CA certificate. `${project.ca_cert}`/
      `${project.ca_key}` do the same for the project's own intermediate
      CA cert/key, and `${project.http_proxy_addr}` for the proxy's own
      `host:port` - useful for a tool that only takes these as a
      command-line flag (`curl --cacert ${project.root_cert} --proxy
      ${project.http_proxy_addr}`), not via `SSL_CERT_FILE`/`HTTP_PROXY`.
- [ ] A `setup` step's own `with` block is rendered too, before its
      `Export` result reaches a cross-scope consumer - e.g. a `setup` step
      using `${project.root_cert}` in one of its own `export` values, read
      back by an `env` step via `${setup.<name>.out.<key>}`.
- [ ] A step whose `with` block references `${needs.<step>...}` or
      `${setup.<name>...}` without also listing that name in its own
      `needs` fails `kevin validate` - both facts are static in the file,
      so this is caught before `kevin run`/`kevin setup` touch Docker, not
      only at the point that step's `with` block actually renders.
      (Automated at the unit level - `internal/config/config_test.go`'s
      `TestValidateNeedsReferences`.)

## 13. Plugin packaging: pack / push / trust / signed

_Automated by `gnob e2e` (`tests/e2e/plugin_test.go`), minus the minisign and
oci parts, which need a signing key and a registry reachable over HTTPS -
those stay manual._

Using the echo plugin as the guinea pig:

```sh
mkdir -p /tmp/kevin-pkg/dist
cp bin/kevin-plugin-echo /tmp/kevin-pkg/dist/
kevin plugin pack /tmp/kevin-pkg/dist -o /tmp/kevin-pkg/echo.tar.gz \
  --name echo --version 1.0.0 --entrypoint kevin-plugin-echo
```

- [ ] Produces `echo.tar.gz` containing `manifest.json` plus the entrypoint
      binary - `manifest.json` is never written to the source dir itself,
      only into the archive; prints `echo 1.0.0 -> /tmp/kevin-pkg/echo.tar.gz`.

`file:` source, unsigned:

```cue
plugins: echo: file: "/tmp/kevin-pkg/echo.tar.gz"
```

- [ ] An env using `echo:echo` with this `plugins:` entry runs correctly;
      `.kevin/plugins/echo/` gets extracted once, and a second run with the
      archive unchanged skips re-extraction (check mtime/log line).
- [ ] Add a `checksum: "sha256:..."` (wrong digest) - fails closed before
      extraction. Fix the digest - succeeds.

Signing:

```sh
minisign -Sm /tmp/kevin-pkg/echo.tar.gz     # writes echo.tar.gz.minisig
minisign -f -p /tmp/kevin-pkg/echo.pub -s ~/.minisign/minisign.key   # if no key yet: minisign -G
kevin plugin trust add /tmp/kevin-pkg/echo.pub
```

- [ ] `trust add` prints a key ID.
- [ ] `kevin plugin trust list` shows it.

```cue
plugins: echo: {
    file:   "/tmp/kevin-pkg/echo.tar.gz"
    signed: true
}
```

- [ ] Env using this entry runs successfully (valid signature, trusted key).
- [ ] `kevin plugin trust remove <key-id>`, rerun - now fails closed with a
      "no such key in the trust store" error, refuses to extract.
- [ ] Delete the `.minisig` file entirely (trust re-added) - fails with a
      clear "signed: true but the package has no signature" error, not a
      silent skip.

`oci:`/`http:` sources (needs a registry/HTTP server reachable, e.g.
`python3 -m http.server` for `http:`). For `oci:`, kevin's registry client
always uses HTTPS with no plain-HTTP/insecure option - a bare
`docker run -d -p 5000:5000 registry:3` (plain HTTP, no TLS) will not work.
Use a registry that terminates TLS with a certificate this machine trusts
(e.g. a real registry you can `docker login` to), or skip this part:

```sh
kevin plugin push /tmp/kevin-pkg/echo.tar.gz localhost:5000/echo:v1
```

- [ ] Prints the pushed digest, and (since the `.minisig` sibling exists)
      pushes the signature too, printing its digest as well.

```cue
plugins: echo: oci: "localhost:5000/echo:v1"
```

- [ ] Resolves and extracts correctly; a second project pointed at the same
      digest reuses `~/.kevin/pkg-cache/` instead of re-fetching (delete the
      registry/stop the server, confirm the second run still works from
      cache).

```cue
plugins: echo: http: "http://localhost:8000/echo.tar.gz"
```

(serve `/tmp/kevin-pkg` with `python3 -m http.server 8000` from that dir)

- [ ] Works the same way; with no `checksum`, confirm it re-downloads every
      run (e.g. touch a log line or watch network activity) rather than
      trusting a stale cache entry.

```sh
kevin plugin list
```

- [ ] Prints every builtin step type as `builtin:<name>` (`builtin:container`,
      `builtin:kind`, `builtin:kubectl`, `builtin:helm`,
      `builtin:wait`, `builtin:route`, `builtin:exec`), one per line.

## 14. Reserved plugin namespace

_Automated by `gnob e2e` (`tests/e2e/cli_test.go`)._

```cue
plugins: kevin: {cmd: "./anything"}
```

- [ ] Fails validation: `kevin` (and `builtin`, `cmd`, `core`, `docker`,
      `file`, `helm`, `http`, `k8s`, `kubectl`, `kubernetes`, `oci`,
      `official`, `std`) can't be used as a `plugins:` key.

## 15. Environment file formats

_Automated by `gnob e2e` (`tests/e2e/cli_test.go`)._

Convert `examples/echo/kevin.cue` to YAML and JSON by hand (or via `cue
export`) and confirm each runs identically:

- [ ] `kevin.yaml` - same DAG behavior as the `.cue` original.
- [ ] `kevin.json` - same.
- [ ] A dotfile variant (`.kevin.cue`) is picked up the same as the
      non-dotted name.
- [ ] Two environment files of different formats in the same directory
      (e.g. `kevin.cue` and `kevin.yaml` both present) - fails clearly
      ("exactly one" environment file allowed), not a silent pick of one.

## 16. Crash resilience / idempotent teardown

_The crash-resilience part is automated by `gnob e2e`
(`tests/e2e/lifecycle_test.go`). The `kevin ca install` idempotency check
below installs into the machine's trust store, so it stays manual along
with the rest of section 4._

```sh
kevin -C examples/web run &
sleep 5
kill -9 %1        # simulate a crash, not Ctrl-C
```

- [ ] Containers labeled `kevin.project=web-example` are still running
      (`docker ps`).

```sh
kevin -C examples/web run
```

- [ ] Second `run` after the crash still succeeds: state is derived from live
      Docker labels, not a state file, so it either reconciles cleanly or the
      leftover containers get cleaned up as part of coming up again (confirm
      no port/name collision error).

```sh
kevin ca install && kevin ca install
```

- [ ] Running `install` twice in a row is a no-op the second time, not a
      duplicate-install error (CA re-derivation, not a saved list).

## 17. `builtin:exec`

_Automated by `gnob e2e` (`tests/e2e/exec_test.go`)._

No Docker - `exec` runs its command directly on the host.

```cue
env: {
	a: {uses: "builtin:exec", with: up: command: ["sh", "-c", "echo hello-from-exec"]}
	b: {
		uses:  "builtin:exec"
		needs: ["a"]
		with: up: command: ["sh", "-c", "echo got: ${needs.a.out.stdout}"]
	}
}
```

- [ ] `b`'s log line reads `got: hello-from-exec` - a dependent step reads
      an exec step's trimmed stdout as its `stdout` output, the same as
      any other step's `Outputs`.

Add a `down` command to `a`:

```cue
a: {
	uses: "builtin:exec"
	with: {
		up:   command: ["sh", "-c", "echo up-ran"]
		down: command: ["sh", "-c", "echo down-ran"]
	}
}
```

- [ ] `Ctrl-C` runs `down`'s command - `down-ran` appears in the log.
      Remove `down` entirely and rerun - teardown logs nothing for `a`, no
      command runs at all.

```cue
up: command: [
    "curl", "--cacert", "${project.root_cert}",
    "--proxy", "${project.http_proxy_addr}",
    "https://internal.example.com",
]
```

- [ ] `proxy: true` on the step adds `HTTP_PROXY`/`HTTPS_PROXY`/
      `SSL_CERT_FILE` to `up`'s (and `down`'s) own environment, built from
      the host-reachable proxy address - not the container-oriented one a
      `builtin:container` step's `proxy: true` uses.

## 18. Plugin-exposed MCP tools

_Automated by `gnob e2e` (`tests/e2e/mcp_test.go`)._

With any environment up that uses a step type implementing `ToolProvider`
(the echo plugin's `echo:echo` ships a demo tool - see
[docs/site/content/docs/extending/writing-a-plugin.md](site/content/docs/extending/writing-a-plugin.md)):

```sh
claude mcp add --transport http kevin http://127.0.0.1:<console-port>/_mcp
```

- [ ] An MCP client's tool list shows the plugin's tool alongside the five
      builtin ones, namespaced `<plugin>_<type>_<tool>` (e.g.
      `echo_echo_echo`), with a required `step` string property injected
      into its schema.
- [ ] Calling it with a valid `step` reaches the real plugin process and
      returns its actual result as structured content, not an error.
- [ ] Calling it with a `step` that doesn't exist, or one of the wrong
      step type, is a clear error, not a silent empty result.

## 19. `builtin:container` expose with `relay: true`

_Automated by `gnob e2e` (`tests/e2e/container_test.go`)._

`internal/version.String` on a checked-out release tag makes kevin default
to the matching `ghcr.io/justenwalker/kevin/relay` image - build a fresh
local one first for anything not yet released, `builtin:container`'s
`relay: true` included:

```sh
./build/gnob relay-image
```

```cue
env: {
	web: {
		uses:  "builtin:container"
		label: "Web Server"
		with: {
			image:  "nginx:alpine"
			expose: [{port: 80, name: "web", relay: true}]
		}
	}
	web_ready: {
		uses:  "builtin:wait"
		label: "Web Ready"
		needs: ["web"]
		with: tcp: address: "${needs.web.system.expose_web}"
	}
}
```

```sh
KEVIN_RELAY_IMAGE=kevin-relay:dev kevin -C /path/to/this run
```

- [ ] `web_ready` reaches `Ready` - the `expose_web` system output (a
      `socks5://<relay>/web:80` upstream) is dialable through the relay's
      SOCKS5 gateway, the same way `examples/kind`'s `apiserver_ready`
      proves `builtin:kind`'s own expose entries.
- [ ] `docker inspect --format '{{json .NetworkSettings.Ports}}'
      kevin-<project>-web` shows no `HostPort` - a `relay: true` entry never
      gets a `docker --publish` spec, unlike a plain `expose` entry.
- [ ] Add a `builtin:exec` step needing `web` that curls
      `http://${needs.web.system.forward_web}/` - the `forward_web` system
      output (a plain host:port the engine's own local forward publishes on
      loopback) reaches the same container with no SOCKS5-aware client
      needed.
- [ ] `expose: [{port: 53, protocol: "udp", relay: true}]` fails validation
      up front - the relay's gateway is SOCKS5 CONNECT, TCP only.

## 20. `examples/s3-app` - persistent cluster, intercepted S3, cross-scope route

_Not yet automated - combines sections 7, 8, and 12 (`builtin:kind` +
`external: true` interception + `setup`/`env` cross-scope `needs`) into one
environment; each is covered separately elsewhere, but not together._

```sh
kevin -C examples/s3-app setup      # once: cluster + ministack + a seeded bucket
kevin -C examples/s3-app run        # every iteration: deploy/redeploy the app
```

- [ ] `setup` brings up `cluster` (`builtin:kind`, `relay: true`),
      `ministack`, and a `seed` Job that populates a bucket - all `setup`
      scope, meant to outlive any single `run`.
- [ ] `s3_intercept` (`env` scope) registers
      `s3.us-east-1.amazonaws.com`/`*.s3.us-east-1.amazonaws.com` as
      `external: true` routes via `${setup.cluster.out.relay_addr}` - a
      cross-scope `needs: ["setup.cluster"]` read entirely through
      `cluster`'s `Export` RPC, with no `kevin setup` process still running.
- [ ] `app` (`builtin:helm`) deploys against the persistent cluster and
      reaches the real `s3.us-east-1.amazonaws.com` hostname with the
      unmodified `aws-cli` - no `--endpoint-url` - landing on `ministack`
      instead of the real internet.
- [ ] `KUBECONFIG=examples/s3-app/.kevin/kubeconfig/s3app kubectl logs -f
      deployment/app` shows a fresh heartbeat each run, plus the bucket
      content `seed` wrote during `setup` - proof the cluster and
      MiniStack never went away between runs.
- [ ] `Ctrl-C` on `run` removes only `app` and `s3_intercept`; the cluster
      and MiniStack are still there afterward (`kubectl get pods` against
      the same kubeconfig).
- [ ] `kevin -C examples/s3-app run` a second time redeploys `app` against
      the same cluster with no `setup` step recreated.
- [ ] `kevin -C examples/s3-app teardown` removes the persistent scope.

