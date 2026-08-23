---
title: "🔑 CA and trust store"
weight: 2
---

# CA and trust store

kevin signs every certificate the proxy presents with its own CA, so it never needs to touch a real one. There are two levels:

| Level   | Location        | Signs                       |
|---------|------------------|-------------------------------|
| Root    | `~/.kevin/`      | the authority of a project    |
| Project | `./.kevin/` (`./.kevin/<name>/` for a named environment) | a leaf certificate per host   |

Only the root ever reaches a trust store, and it's installed there once for the whole machine, so a trust store holds one kevin anchor no matter how many kevin projects exist. Each project's own authority lives in the project directory and goes away with it.

## Trust setup

To drop `--cacert` from every `curl` and have your browser trust the environment automatically, install the root into the trust stores of the machine:

```sh
kevin -C <project> setup       # install
kevin -C <project> teardown    # remove
```

Run this once for the machine; you don't need to repeat it per project. Use [`builtin:trust`]({{< relref "/docs/reference/trust" >}}) as a `setup` step to do this (see [`examples/trust`](https://github.com/justenwalker/kevin/tree/main/examples/trust)):

```cue
setup: ca: {
    uses: "builtin:trust"
    with: {
        system:  false  // the user's trust store; true needs root
        firefox: true   // also install into Firefox's own certificate database
    }
}
```

The default installs into the trust store of the **user**, which needs no root; macOS still asks you to confirm the change. Setting `system: true` writes the machine-wide store, which needs root. kevin never asks for a password itself; it prints the exact command to run under `sudo`.

Firefox keeps its own certificate database separate from the OS. That needs `certutil` from the `nss` package, which does not ship with Firefox itself. If it's absent, kevin reports a skip for Firefox rather than failing the whole step. Install it with:

```sh
brew install nss                  # macOS; certutil lands under $(brew --prefix nss)/bin, not on PATH by default
sudo apt install libnss3-tools    # Debian/Ubuntu
sudo dnf install nss-tools        # Fedora
```

`kevin teardown` removes what `setup` installed, and is safe to run without also caring whether `setup` actually ran. It's always safe to call.
