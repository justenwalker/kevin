---
title: "Publishing a plugin"
description: "Package, sign, and push a plugin, and list it in an index."
weight: 3
---

# Publishing a plugin

This guide starts from a built plugin binary. See [Writing a plugin]({{< relref "writing-a-plugin" >}}) to build one.

## Package the plugin

Put the binary and any files it needs in one directory, then run:

```sh
kevin plugin pack ./dist/echo \
  -o ./dist/kevin-plugin-echo.tar.gz \
  --name echo --version 1.0.0 --entrypoint kevin-plugin-echo
```

`pack` writes a `manifest.json` into the archive:

```json
{
    "$v": 1,
    "name": "echo",
    "version": "1.0.0",
    "entrypoint": "kevin-plugin-echo"
}
```

If the directory already has a `manifest.json`, the flags replace its fields. `--name` must match the `plugins:` key that users give the plugin. See [`kevin plugin pack`]({{< relref "/docs/reference/commands/plugin#kevin-plugin-pack-dir" >}}) for every flag.

## Sign the package

Use one scheme.

**minisign.** Sign with your minisign secret key:

```sh
minisign -Sm ./dist/kevin-plugin-echo.tar.gz
```

This writes `kevin-plugin-echo.tar.gz.minisig`. Give users your public key file.

**sigstore.** Sign with [`cosign`](https://docs.sigstore.dev/cosign/system_config/installation/). cosign opens a browser for an OIDC login, or uses the credentials of a CI job:

```sh
cosign sign-blob --yes \
  --bundle ./dist/kevin-plugin-echo.tar.gz.sigstore.json \
  ./dist/kevin-plugin-echo.tar.gz
```

Tell users the identity (an email, or a GitHub Actions workflow URI) and the issuer URL of the certificate.

## Publish the package

To an OCI registry, after `docker login`:

```sh
kevin plugin push ./dist/kevin-plugin-echo.tar.gz ghcr.io/acme/kevin-plugin-echo:v1
```

`push` also uploads a `.minisig` or `.sigstore.json` file next to the archive. Push only one of them: a registry holds one signature for each package digest.

To publish over HTTP, put the signature file at the archive's URL with the signature suffix added, for example `https://example.com/echo.tar.gz` and `https://example.com/echo.tar.gz.minisig`.

## List the plugin in an index

1. In the index repository, add `plugins/<name>/plugin.yaml` if the plugin is new:

   ```yaml
   name: echo
   summary: prints a message
   homepage: https://github.com/acme/kevin-plugin-echo
   signers:
     - scheme: minisign
       key: |
         untrusted comment: echo release key
         RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
   ```

   A `minisign` signer's `key` is the content of your public key file.

2. For each release, add `plugins/<name>/versions/<version>.yaml`. Do not change it after you publish it:

   ```yaml
   version: 1.1.0
   source:
     oci: ghcr.io/acme/kevin-plugin-echo:v1.1.0
     signing:
       scheme: minisign
   ```

3. Commit and push.

See [Plugin index format]({{< relref "/docs/reference/plugin-index" >}}) for every field.

## Publish versions from a separate repository

To let release automation publish versions without write access to the index:

1. Set `version_source` in `plugin.yaml`:

   ```yaml
   version_source: https://github.com/acme/kevin-plugin-echo-releases
   ```

2. In that repository, add `kevin-index.yaml` with `layout: 1`, and add version files at `plugins/<name>/versions/<version>.yaml`.

3. Sign each version file with a signer from `plugin.yaml`:

   ```sh
   minisign -Sm plugins/echo/versions/1.1.0.yaml
   # or
   cosign sign-blob --yes --bundle plugins/echo/versions/1.1.0.yaml.sigstore.json plugins/echo/versions/1.1.0.yaml
   ```

   `kevin plugin index update` skips a version file with no valid signature.

## Related

- [Plugin trust]({{< relref "/docs/concepts/plugin-trust" >}}): why signers live in `plugin.yaml`, and what `version_source` protects against.
- [Third-party plugins]({{< relref "/docs/guides/third-party-plugins" >}}): the user side of these steps.
