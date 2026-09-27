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

To publish over HTTP, put the archive and its signature file at the same URL path, for example `https://example.com/echo.tar.gz` and `https://example.com/echo.tar.gz.minisig`.

## List the plugin in an index

An index is a git repository with this layout:

```
kevin-index.yaml
plugins/
  echo/
    plugin.yaml
    versions/
      1.0.0.yaml
      1.1.0.yaml
```

`kevin-index.yaml` contains one line:

```yaml
layout: 1
```

### `plugin.yaml`

| Field | Required | Description |
|:------|:--------:|:------------|
| `name` | yes | Plugin name. Lowercase letters, digits, and hyphens. |
| `summary` | yes | One line, shown by `kevin plugin search`. |
| `homepage` | no | URL. |
| `maintainer` | no | Person or organization. |
| `signers` | no | Signers that `kevin plugin index install` adds to the user's trust store. |
| `version_source` | no | URL of a separate git repository that holds this plugin's `versions/` directory. |

```yaml
name: echo
summary: prints a message
homepage: https://github.com/acme/kevin-plugin-echo
maintainer: Acme Inc
signers:
  - scheme: minisign
    key: |
      untrusted comment: echo release key
      RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
  - scheme: sigstore
    identity: ci@acme.example
    issuer: https://token.actions.githubusercontent.com
```

A `minisign` signer's `key` is the content of the public key file.

### Version files

Add one file for each release, named `versions/<version>.yaml`. Do not change a version file after you publish it.

| Field | Required | Description |
|:------|:--------:|:------------|
| `version` | yes | Semantic version. Must match the file name. |
| `source` | yes | The same fields as a `plugins:` entry in `kevin.cue`: `oci`, `file`, or `http`, with `signing` and `checksum`. `cmd` is not allowed. |

```yaml
version: 1.1.0
source:
  oci: ghcr.io/acme/kevin-plugin-echo:v1.1.0
  signing:
    scheme: minisign
```

The latest version is the highest version with no pre-release suffix. If every version has a suffix (such as `1.0.0-rc1`), the latest is the highest of those.

### Version files in a separate repository

To let release CI publish versions without write access to the index, set `version_source` in `plugin.yaml`:

```yaml
version_source: https://github.com/acme/kevin-plugin-echo-releases
```

The `version_source` repository uses the same layout: `kevin-index.yaml` and `plugins/<name>/versions/`. Each version file needs a signature file next to it, from a signer in `plugin.yaml`:

```sh
minisign -Sm plugins/echo/versions/1.1.0.yaml
# or
cosign sign-blob --yes --bundle plugins/echo/versions/1.1.0.yaml.sigstore.json plugins/echo/versions/1.1.0.yaml
```

`kevin plugin index update` skips a version file with no valid signature, and reports it. `version_source` requires at least one entry in `signers`.

## Related

- [Plugin trust]({{< relref "/docs/concepts/plugin-trust" >}}): why signers live in `plugin.yaml`, and what `version_source` protects against.
- [Third-party plugins]({{< relref "/docs/guides/third-party-plugins" >}}): the user side of these steps.
