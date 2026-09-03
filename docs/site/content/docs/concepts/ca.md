---
title: "Certificate authority"
description: "The root and per-project intermediate CAs, and how kevin manages the trust store."
weight: 8
---

# Certificate authority

kevin creates the authorities and holds the private keys itself. The proxy needs a private key to sign a leaf, thus the authority does not live in a plugin. Every key is ECDSA P-256, at mode 0600 in a directory at mode 0700.

There are two levels.

| Level | Location | Subject | Signs |
| --- | --- | --- | --- |
| Root | `~/.kevin/` | `Kevin Local Root CA` | the authority of a project |
| Project | `./.kevin/` (`./.kevin/<name>/` for a named environment) | `Kevin Local Intermediate CA - Project <name>` | a leaf for each host |

Only the root reaches a trust store, and it reaches it one time for the machine. A trust store therefore holds one kevin anchor however many projects exist. Each project signs with its own key, which lives in the project directory and goes when the directory goes.

The certificate file of a project holds the chain: the authority of the project, then the root. The proxy appends this same chain after every leaf it mints, thus a client that trusts the root alone can build the chain.

kevin checks the signature of the authority of the project against the root on every use. A user who deletes the home directory gets a new root, and the stale authority of the project is replaced rather than served.

`kevin ca install`/`uninstall` manage the trust store directly, outside the DAG entirely - the root names no project, so there's no per-project `setup`/`teardown` scope for it to live in. `kevin ca install` installs the root certificate into the keychain of macOS, the anchor directory of Linux, and the NSS database of each Firefox profile.

A store that this machine does not have is a skip, not a failure. A machine without `certutil` reports that Firefox will not trust the authority, and the command continues.

The default is the trust store of the **user**, which needs no root. macOS still asks the user to confirm the change to the trust settings. `--system` writes the machine-wide store, which needs root. `kevin ca install` never asks for a password itself: it reports the exact command, and the user runs it.

There is no state file, thus `kevin ca uninstall` must be idempotent and must derive what it removes from the trust stores themselves. It matches on the root's fixed subject name, a constant - not a per-project subject the way a DAG step's `Down` would need to derive from the environment - so a second call is naturally a no-op rather than a duplicate-removal error.
