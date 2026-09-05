---
title: "CA and trust store"
description: "How kevin's CA signs certificates for the proxy, and how to trust it."
weight: 2
---

# CA and trust store

kevin signs every certificate the proxy presents with its own CA, so it never needs to touch a real one. There are two levels:

| Level   | Location        | Signs                       |
|---------|------------------|-------------------------------|
| Root    | `~/.kevin/`      | a project's own intermediate CA |
| Project | `./.kevin/` (`./.kevin/<name>/` for a named environment) | a leaf certificate per host   |

Only the root ever reaches a trust store, and it's installed there once for the whole machine, so a trust store holds one kevin anchor no matter how many kevin projects exist. Each project's own authority lives in the project directory and goes away with it.

A [`builtin:route`]({{< relref "/docs/reference/steps/route" >}}) entry with `skip_mitm: true` never touches this CA at all: its traffic tunnels straight through to the upstream's own certificate instead of a kevin-signed leaf, so a client needs the upstream's own CA trusted, not kevin's.

## Trust setup

To drop `--cacert` from every `curl` and have your browser trust the environment automatically, install the root into the trust stores of the machine:

```sh
kevin ca install      # install
kevin ca uninstall    # remove
```

Run this once for the machine, with no project involved at all - the root names no project, so there's nothing to repeat per project. `kevin ca install` generates the root the first time it runs, then adds it to the trust stores of this machine:

```sh
kevin ca install --system=false --firefox   # the defaults
```

The default installs into the trust store of the **user**, which needs no root; macOS still asks you to confirm the change. Passing `--system` writes the machine-wide store instead, which needs root. kevin never asks for a password itself; it prints the exact command to run under `sudo`.

Firefox keeps its own certificate database separate from the OS. That needs `certutil` from the `nss` package, which does not ship with Firefox itself. If it's absent, kevin reports a skip for Firefox rather than failing. Install it with:

```sh
brew install nss                  # macOS; certutil lands under $(brew --prefix nss)/bin, not on PATH by default
sudo apt install libnss3-tools    # Debian/Ubuntu
sudo dnf install nss-tools        # Fedora
```

`kevin ca uninstall` removes what `install` added, and is safe to call whether or not `install` ever ran - the second run of either command reports a skip, not an error.
