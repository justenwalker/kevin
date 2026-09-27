---
title: "Plugin trust"
description: "How kevin decides whether to run a plugin package: checksums, signatures, trust stores, and indexes."
weight: 11
---

# Plugin trust

A plugin is a program that kevin runs on your machine. kevin has three ways to decide whether a downloaded package is the one you meant to run.

## Checksums and digests

A `checksum` on a `file` or `http` entry, or a digest in an `oci` reference, pins the exact bytes of one package. Nothing else can match. The cost is that every new release needs an edit to `kevin.cue`.

## Signatures

`signing` pins who built the package instead of which bytes it contains. A moving tag such as `:v1` can then get new releases with no edit to `kevin.cue`.

kevin supports two schemes:

- **minisign** checks a signature against a public key. kevin verifies minisign signatures itself. Signing is done with the `minisign` tool, so kevin never handles a secret key.
- **sigstore** checks a short-lived certificate that records the OIDC identity that signed, such as a specific CI workflow. This answers a question that a long-lived key cannot: which workflow built this package. kevin runs `cosign` to verify, and does not handle the OIDC token or certificate. Verification checks the transparency log proof in the bundle, with no call to the log. cosign can refresh its public trust root from the network before the proxy starts, so that traffic is not subject to egress control.

## Why trust stores are outside `kevin.cue`

The trusted keys and identities are in `~/.kevin/trusted-keys/` and `~/.kevin/trusted-identities/`, not in `kevin.cue`. Anyone who can edit `kevin.cue` can already change which package it names. If the same file also listed trusted signers, that person could name their own package and trust their own key.

For sigstore this matters more. The certificate authority issues a certificate to any authenticated identity, so anyone can produce a valid signature for their own identity. The `identity` and `issuer` in `kevin.cue` say who should have signed. The trust store says who you agreed to trust. kevin requires both to match.

## Indexes

An index tells you that a plugin exists and where its releases are. Adding an index adds nothing to your trust store. Its `plugins:` entries go through the same checksum and signature checks as an entry you wrote yourself.

`kevin plugin index install` is the one command that changes the trust store. It adds only the signers listed in the plugin's `plugin.yaml`.

### Signers in `plugin.yaml`

Signers are in `plugin.yaml`, which changes rarely, and not in the version files, which release automation adds often. A compromised release job can add a version file, but it cannot add a signer without a separate, visible change to `plugin.yaml`.

### Separate version repositories

If an attacker controls the whole index repository, they can change both `plugin.yaml` and the version files. `version_source` moves the version files to a different repository and requires each one to have a signature from a signer in the index. An attacker then needs control of both repositories, or a signer's key, to publish a release that kevin accepts.

### Append-only layout

Each plugin is a directory, and each release is a new file. Release automation adds a file and never edits an existing one, so two releases published at the same time do not conflict.
