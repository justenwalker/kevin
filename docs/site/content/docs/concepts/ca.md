---
title: "Certificate authority"
description: "The root and project CAs, and how kevin manages the trust store."
weight: 8
---

# Certificate authority

The proxy terminates TLS, so it needs a private key to sign a certificate for each host. kevin creates and holds its CAs itself for this reason. Every key is ECDSA P-256, stored with mode 0600 in a directory with mode 0700.

## Two levels

| Level | Location | Subject | Signs |
|:------|:---------|:--------|:------|
| Root | `~/.kevin/` | `Kevin Local Root CA` | Each project CA. |
| Project | `.kevin/` in the project directory (`.kevin/<name>/` for a named environment) | `Kevin Local Intermediate CA - Project <name>` | A certificate for each host the proxy serves, and the certificates of the relay control channel. |

Only the root goes into a trust store, once per machine. A trust store holds one kevin certificate however many projects exist. Each project signs with its own key, which is in the project directory and is deleted with it.

The project certificate file holds the chain: the project CA, then the root. The proxy sends this chain after every certificate it signs, so a client that trusts only the root can verify it.

The relay control channel uses the same project CA. kevin signs a short-lived server certificate for the relay and a client certificate for itself. Only a client with a certificate from this project's CA can send commands to the relay. See [Relay]({{< relref "/docs/concepts/relay#control-channel" >}}).

## Checking the project CA

On each run, kevin checks that the project CA was signed by the current root. If you delete `~/.kevin/`, kevin creates a new root, and replaces the project CA instead of using one that the new root did not sign.

## The trust store

`kevin ca install` and `kevin ca uninstall` are not part of a project. The root is the same for every project, so it has no `setup` scope to belong to.

`install` adds the root to the user trust store by default, which needs no root privileges. `--system` uses the machine-wide store, which does. kevin never asks for a password: it prints the command for you to run.

A trust store that is not on the machine is skipped, not an error. Without `certutil`, kevin reports that Firefox will not trust the CA, and continues.

kevin keeps no record of what it installed. `uninstall` finds kevin certificates by the root's subject name, which is the same on every machine, so running it twice is safe.
