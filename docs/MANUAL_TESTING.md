# Manual test plan

Checks that no test runs. Everything else runs in `./build/gnob test`,
`./build/gnob integration`, or `./build/gnob e2e` (see
[contributing](site/content/docs/contributing.md)).

- **Part 1** holds checks that cannot be automated: they change the machine's
  trust store, need a person's eyes, need a browser other than headless Chrome,
  or need an interactive login.
- **Part 2** holds checks that could be automated but are not yet. Delete an item
  when a test covers it.

Run each item against a release candidate, or pick one after a change to the area
it covers. Check it off only after you have seen the expected result, not just
that the command exited zero.

Prerequisites: Docker daemon running, this repo cloned, nothing else bound to
ports 18080-18082.

```sh
go generate -C ./build -tags gnob .
./build/gnob build
export PATH="$PWD/bin:$PATH"
```

`kevin` and `kevin-plugin-echo` are on `PATH` from `bin/`. `kevin-relay` only
builds as a Docker image (`./build/gnob relay-image`, tagged `kevin-relay:dev`).

After any unreleased change to the relay (`cmd/kevin-relay`, `internal/relay`, or
the control channel protocol), pin the image you built:

```sh
./build/gnob relay-image
export KEVIN_RELAY_IMAGE=kevin-relay:dev
```

Without it, kevin picks the last released relay image, and a step fails with
`authentication handshake failed: tls: first record does not look like a TLS
handshake`, or the run hangs.

Before starting, clear any leftover state from previous manual runs so a
stale workspace doesn't mask a real bug:

```sh
git status examples   # every examples/*/.kevin should be untracked/ignored
rm -rf examples/*/.kevin
```

Two items need a packed plugin. Build it once:

```sh
mkdir -p /tmp/kevin-pkg/dist
cp bin/kevin-plugin-echo /tmp/kevin-pkg/dist/
kevin plugin pack /tmp/kevin-pkg/dist -o /tmp/kevin-pkg/echo.tar.gz \
  --name echo --version 1.0.0 --entrypoint kevin-plugin-echo
```

## Part 1: Cannot be automated

### CA and trust store (`kevin ca`)

These change the real trust store of the machine, so no test runs them.

```sh
kevin ca install
```

- [ ] Works with no `kevin.cue` in the cwd - no project needed.
- [ ] Installs into the user's trust store (no root needed); on macOS,
      prompts for confirmation of the trust settings change.
- [ ] If `certutil` (nss) is present, also installs into Firefox's own DB; if
      absent, prints a skip for Firefox rather than failing.
- [ ] Running `kevin ca install && kevin ca install` is a no-op the second
      time, not a duplicate-install error (CA re-derivation, not a saved list).

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

### Browsers and terminals

- [ ] `kevin -C examples/web run` in an actual terminal (not piped): the live
      list looks right - one row per step, its state, a spinner, and a progress
      bar once an estimate exists. Nothing from an earlier frame is left behind
      when rows change.
- [ ] Point Firefox and Safari (not Chrome, which a test covers) at
      `http://127.0.0.1:18080/proxy.pac` as the auto-config URL, then visit
      `https://web.kevin.home/` - loads the page (after accepting or installing
      the cert, see the CA and trust store item), and a normal site such as
      `https://example.com/` still loads directly, unaffected.
- [ ] `kevin -C examples/web run --open` launches the console in the OS default
      browser. (A test checks that kevin hands the URL to the opener, not that a
      real browser opens.)

### Sigstore keyless signing

Keyless signing needs an interactive OIDC login and `cosign` on `PATH`, so
no test can produce a real bundle. A test covers the minisign scheme the same
way, with a generated key.

```sh
cosign sign-blob --yes --bundle /tmp/kevin-pkg/echo.tar.gz.sigstore.json /tmp/kevin-pkg/echo.tar.gz
```

- [ ] Prints a device-flow URL and code - open it, log in, and it writes
      `echo.tar.gz.sigstore.json` next to the archive.
- [ ] Read the identity/issuer it signed with:
      `openssl x509 -in <(python3 -c "import json,base64,sys; d=json.load(open('/tmp/kevin-pkg/echo.tar.gz.sigstore.json')); sys.stdout.buffer.write(base64.b64decode(d['verificationMaterial']['certificate']['rawBytes']))") -noout -text | grep -A1 "Subject Alternative Name"`
      shows the signing identity (e.g. an email); the `1.3.6.1.4.1.57264.1.1`
      OID line shows the OIDC issuer.

```sh
kevin plugin trust add-identity --identity <identity> --issuer <issuer>
```

```cue
plugins: echo: {
    file: "/tmp/kevin-pkg/echo.tar.gz"
    signing: {
        scheme:   "sigstore"
        identity: "<identity>"
        issuer:   "<issuer>"
    }
}
```

- [ ] An env using this entry runs successfully (valid bundle, trusted
      identity).
- [ ] `kevin plugin trust remove-identity <identity> <issuer>`, rerun - fails
      closed with "signing identity isn't trusted", refuses to
      extract.
- [ ] Re-add the identity, then append a byte to `echo.tar.gz` (tampering it
      after signing) - fails closed with "sigstore signature doesn't verify
      against its package", not a silent skip. Restore the original file
      afterward.
- [ ] Rename `cosign` off `PATH` temporarily (or unset `PATH`) - fails
      closed with "needs cosign to verify its sigstore signature", not a
      hang or an unrelated error.

#### Plugin index with a sigstore-signed version source

Two local git fixtures: one plain (`index`) holding `plugin.yaml`, one
(`releases`) holding the plugin's `versions/` tree instead.

```sh
mkdir -p /tmp/kevin-fed/index/plugins/demo
mkdir -p /tmp/kevin-fed/releases/plugins/demo/versions
echo "layout: 1" > /tmp/kevin-fed/releases/kevin-index.yaml
cat > /tmp/kevin-fed/releases/plugins/demo/versions/1.0.0.yaml <<'EOF'
version: 1.0.0
source:
  oci: ghcr.io/example/kevin-plugin-demo:v1.0.0
EOF
git -C /tmp/kevin-fed/releases init -q -b main
git -C /tmp/kevin-fed/releases add -A
git -C /tmp/kevin-fed/releases commit -q -m "1.0.0"
```

Sign the release with a real sigstore (keyless) signature:

```sh
cosign sign-blob --yes --bundle /tmp/kevin-fed/releases/plugins/demo/versions/1.0.0.yaml.sigstore.json \
  /tmp/kevin-fed/releases/plugins/demo/versions/1.0.0.yaml
git -C /tmp/kevin-fed/releases add -A
git -C /tmp/kevin-fed/releases commit -q -m "sign 1.0.0"
```

- [ ] Opens a device-flow URL - log in, and it writes
      `1.0.0.yaml.sigstore.json` next to the version file. Read the
      identity/issuer it signed with the same `openssl`/`python3` one-liner as
      above, and use that identity/issuer below.

```sh
echo "layout: 1" > /tmp/kevin-fed/index/kevin-index.yaml
cat > /tmp/kevin-fed/index/plugins/demo/plugin.yaml <<EOF
name: demo
summary: a demo plugin with a federated version source
signers:
  - scheme: sigstore
    identity: "<identity>"
    issuer: "<issuer>"
version_source: /tmp/kevin-fed/releases
EOF
git -C /tmp/kevin-fed/index init -q -b main
git -C /tmp/kevin-fed/index add -A
git -C /tmp/kevin-fed/index commit -q -m demo

kevin plugin index add /tmp/kevin-fed/index --as fed
kevin plugin index show demo
```

- [ ] `index add` reports `1 plugins`, no warnings.
- [ ] `index show demo` lists `version 1.0.0 (latest)` and renders its
      snippet - the version file was loaded from `/tmp/kevin-fed/releases`,
      not from `/tmp/kevin-fed/index` at all, verified against the *index*
      repo's own declared signer.

Tamper with the already-signed release and re-run:

```sh
echo "  checksum: sha256:0000000000000000000000000000000000000000000000000000000000000000" \
  >> /tmp/kevin-fed/releases/plugins/demo/versions/1.0.0.yaml
kevin plugin index update
```

- [ ] Reports a warning for `demo` (signature no longer verifies against
      the tampered content) and `index show demo` fails with
      `pluginindex: no such plugin` - the tampered version is excluded
      entirely, not trusted with a note. Revert the file
      (`git -C /tmp/kevin-fed/releases checkout -- plugins/demo/versions/1.0.0.yaml`)
      and confirm `update` recovers it.

## Part 2: Not yet automated

Each item could be a test but is not one yet. Delete an item when a test covers it.

### Run lifecycle and console

- [ ] With `examples/web` up, the console marks `web_route` removed after
      `Ctrl-C`, even though `builtin:route` has no `Down` RPC and prints no
      `removed` line.
