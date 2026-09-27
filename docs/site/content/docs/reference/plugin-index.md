---
title: "Plugin index format"
weight: 5
description: "Files and fields of a plugin index repository."
---

# Plugin index format

A plugin index is a git repository that lists plugins and their releases. `kevin plugin index add` adds one. See [Publishing a plugin]({{< relref "/docs/extending/publishing-a-plugin" >}}) to add a plugin to an index.

## Layout

```text
kevin-index.yaml
plugins/
  <name>/
    plugin.yaml
    versions/
      <version>.yaml
```

`kevin-index.yaml` must contain `layout: 1`. kevin rejects a repository without it, or with another layout number.

## `plugin.yaml`

| Field | Required | Description |
|:------|:--------:|:------------|
| `name` | yes | Plugin name. Lowercase letters, digits, and hyphens. |
| `summary` | yes | One line, shown by `kevin plugin search`. |
| `homepage` | no | URL. |
| `maintainer` | no | Person or organization. |
| `signers` | no | Signers that `kevin plugin index install` adds to the user's trust store, and that must sign version files from a `version_source`. |
| `version_source` | no | URL of a git repository that holds this plugin's `versions/` directory, instead of this repository. Requires `signers`. |

### Signers

| Field | Scheme | Description |
|:------|:-------|:------------|
| `scheme` | both | `minisign` or `sigstore`. |
| `key` | `minisign` | Content of the minisign public key file. |
| `identity` | `sigstore` | Certificate identity, such as an email or a GitHub Actions workflow URI. |
| `issuer` | `sigstore` | OIDC issuer URL. |

## Version files

One file for each release, at `plugins/<name>/versions/<version>.yaml`.

| Field | Required | Description |
|:------|:--------:|:------------|
| `version` | yes | Semantic version, such as `1.4.0` or `1.4.0-rc1`. Must match the file name. |
| `source` | yes | The same fields as a `plugins:` entry in the environment file: one of `oci`, `file`, or `http`, with optional `signing` and `checksum`. `cmd` is not allowed. |

## `version_source` repositories

A `version_source` repository has the same layout, with `kevin-index.yaml` and `plugins/<name>/versions/`. Each version file needs a signature file next to it, from a signer in the index's `plugin.yaml`:

| Scheme | Signature file |
|:-------|:---------------|
| `minisign` | `<version>.yaml.minisig` |
| `sigstore` | `<version>.yaml.sigstore.json` |

kevin skips a version file without a valid signature, and reports it.

## Latest version

The latest version is the highest version with no pre-release suffix. If every version has a suffix, the latest is the highest of those.
