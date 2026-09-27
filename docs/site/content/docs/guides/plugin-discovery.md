---
title: "Plugin discovery"
description: "Finding third-party plugins across configured git repos with kevin plugin index and kevin plugin search."
weight: 6
---

# Plugin discovery

`plugins:` in `kevin.cue` (see [Plugin sources]({{< relref "/docs/environment-file#plugin-sources" >}})) already fetches a third-party plugin package by `oci:`, `file:`, or `http:` reference, once you know it. `kevin plugin index` and `kevin plugin search` solve the problem before that: finding what plugins exist in the first place, without already knowing a registry ref by heart.

It's low-tech and federated on purpose: no server, no curated central registry, no gatekeeping. You point kevin at one or more plain git repos, kevin clones them into a local cache, and you search/list/show across all of them.

**This is a phonebook, not a trust boundary.** `kevin plugin trust` (see [Signing packages]({{< relref "/docs/environment-file#signing-packages" >}})) is still required before kevin will ever fetch a signed package. Adding an index source only makes a plugin *discoverable* - it changes nothing about how kevin fetches or verifies the package once you paste its `plugins:` entry into `kevin.cue`. The one deliberate exception is `kevin plugin index install` (below): it *can* add to your trust store, but only a signer the plugin's own `plugin.yaml` names - never anything a single version file could introduce on its own.

## The repo format

An index repo's root holds a `kevin-index.yaml` marker, sibling to a `plugins/` directory with one subdirectory per plugin, each with its own version history:

```
kevin-index.yaml          # layout: 1
plugins/
  postgres/
    plugin.yaml            # stable identity: name, summary, homepage, maintainer
    versions/
      1.3.0.yaml            # source: {oci/file/http, signing} for that release
      1.4.0.yaml
```

`kevin-index.yaml` just says `layout: 1` - proof the clone is actually shaped like this format, not an unrelated git repo that happens to have a directory called `plugins/`, and a place for a future layout change to declare itself instead of being silently misread as this one. `kevin plugin index add`/`update` refuses a repo (or, below, a `version_source`) that's missing it or names a layout this version of kevin doesn't recognize.

`plugin.yaml` holds a plugin's identity - it only changes for identity edits (description, maintainer, or its signer list below), which is rare:

```yaml
name: postgres
summary: a postgres database for local dev
homepage: https://github.com/acme/kevin-plugin-postgres
maintainer: Acme Inc
signers:
  - scheme: minisign
    key: |
      untrusted comment: postgres release key
      RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
```

`signers` lists the plugin's acceptable release signers - `kevin plugin index install` (below) trusts these automatically. It lives here, on the rarely-edited identity file, deliberately not on a `versions/*.yaml` release file: a single compromised release (the append-only, usually CI-automated path) can't introduce a new "trusted" key on its own, since that would require a distinct, visible edit to `plugin.yaml` instead. A minisign entry carries the signer's public key text directly (the same two-line format `minisign -Sm` and `kevin plugin trust add` already use); a sigstore entry carries `identity`/`issuer`, same as a `kevin.cue` `signing:` block.

Each file under `versions/` is one release, written once and never touched again. Its `version` field must match its own filename stem:

```yaml
# versions/1.4.0.yaml
version: 1.4.0
source:
  oci: ghcr.io/acme/kevin-plugin-postgres:v1.4.0
  signing:
    scheme: minisign
```

`source` takes the same shape as a `kevin.cue` `plugins:` entry - `oci`, `file`, or `http`, with the same `signing` field - so it can never drift from what `kevin.cue` itself accepts. A `cmd:` source isn't valid here; a local binary path means nothing in a remote catalog.

**Every layer of this format is append-only by design**, specifically so a plugin author's release CI can publish a new version as a single `git add` of a brand-new file - never a read-modify-write of anything another release's CI job might touch at the same time:

- one plugin = one directory: adding a plugin never touches another plugin's files.
- one version = one file: cutting a release never touches an existing version's file, or even another release landing in the same directory at the same time.

### Federating the version list

`signers` closes one gap - a single compromised release can't introduce a new trusted key - but it doesn't close a full compromise of the index repo itself: write access to both `plugin.yaml` and `versions/` at once lets an attacker rewrite `signers` to point at their own key, and a later `kevin plugin index install` would trust it with no way to tell it wasn't the original. `version_source` closes that gap too, by moving *what's available* to a different git repo than *who's allowed to sign*:

```yaml
name: postgres
summary: a postgres database for local dev
signers:
  - scheme: minisign
    key: |
      untrusted comment: postgres release key
      RWQf6LRCGA9i53mlYecO4IzT51TGPpvWucNSCh1CBM0QTaLn73Y7GFO3
version_source: https://github.com/acme/kevin-plugin-postgres-releases
```

When `version_source` is set, `postgres`'s `versions/` tree lives in that repo instead of this one - same `kevin-index.yaml` marker and `plugins/postgres/versions/*.yaml` layout, just at a different location (one version-source repo can serve several plugins' releases this way). Every version loaded from there must carry a detached signature next to it - `1.4.0.yaml.minisig` (`minisign -Sm`'s output) or `1.4.0.yaml.sigstore.json` (`cosign sign-blob --bundle`'s output) - verified against the *index* repo's own `signers`, never the machine's global trust store. Compromising the version-source repo alone can't forge a release: without a real signature from an already-declared signer, `kevin plugin index update` excludes that version (and reports why) instead of trusting it. A `version_source` with no `signers` declared at all is a hard error - there'd be nothing to verify against.

## Resolving "latest"

"Latest" - used whenever a command below doesn't ask for a specific `--version` - is always the highest semver among a plugin's `versions/*.yaml` files that isn't a pre-release (`1.4.0-rc1` and friends are excluded, the same convention npm, cargo, and Go modules use), falling back to the highest pre-release only if that's literally all that exists. This is computed from the files present on disk every time, not read from a separate pointer file - one more thing that stays purely append-only.

## Managing index sources

```sh
kevin plugin index add https://github.com/acme/kevin-plugin-index
```

Clones the repo, aliases it (by default, a name derived from the URL - pass `--as` to pick your own), and reports how many plugins it found. Adding a URL that's already configured is a silent no-op - it just hands back the source you already have.

```sh
kevin plugin index list
kevin plugin index remove acme
```

`list` shows every configured source's alias and URL. `remove` drops a source and its cloned repo cache.

```sh
kevin plugin index update
```

Re-clones every configured source from scratch, independently - a repo that fails to clone or has a malformed `plugin.yaml`/version file is reported on its own line and never blocks any other source, or any other plugin in the same repo, from updating. A plugin's own `version_source` (above) is re-cloned the same way, at the same time; an unreachable one, or a version that fails its signature check, is that one plugin's own warning, not a failure for the rest of the repo. The command exits non-zero if anything failed, even though every source was still attempted, so it's safe to run from a script that wants to know.

## Finding and using a plugin

```sh
kevin plugin search postgres
```

Searches every configured source's plugins by name and summary, case-insensitively. With no query, `kevin plugin search` lists everything. Each row is `<alias>/<name>`, its summary, and its latest version.

```sh
kevin plugin index show postgres
```

Prints the plugin's metadata, every known version (newest first), and the latest version's pasteable snippet:

```
name	postgres
summary	a postgres database for local dev
homepage	https://github.com/acme/kevin-plugin-postgres
maintainer	Acme Inc
repo	acme (https://github.com/acme/kevin-plugin-index)
version	1.4.0 (latest)
version	1.3.0

plugins: {
	postgres: {
		oci: "ghcr.io/acme/kevin-plugin-postgres:v1.4.0"
		signing: {
			scheme: "minisign"
		}
	}
}
```

Paste that `plugins:` block straight into `kevin.cue`. To pin an older release instead of the latest, ask for it exactly - no fuzzy matching, since you're expected to copy the version string straight from the list above it:

```sh
kevin plugin index show postgres --version 1.3.0
```

### The `<alias>/<name>` syntax

A bare plugin name that matches more than one configured source is ambiguous - `kevin plugin index show postgres` errors, naming every source that has a plugin called `postgres`. Retype the request scoped to one source:

```sh
kevin plugin index show acme/postgres
```

`kevin plugin search` and `kevin plugin index list` never hit this: they show every match side by side, with its owning alias, instead of forcing a single answer.

## Installing directly

`show` prints a snippet to paste in by hand; `install` does the paste (and the trust setup) for you:

```sh
kevin plugin index install postgres
```

This resolves `postgres` the same way `show` does (`--version` pins an exact release, same no-fuzzy-matching rule, same `<alias>/<name>` disambiguation), trusts every signer the plugin's `plugin.yaml` declares for that release's signing scheme - `kevin plugin trust add`/`add-identity` under the hood, so `kevin plugin trust list` shows the result - and writes the `plugins:` entry straight into your `kevin.cue`, leaving everything else in the file untouched:

```
trusted	0764f8e1f91b2058
installed	postgres	1.4.0
```

Pass `--no-trust` to skip the trust step and leave your trust store as-is - useful if you'd rather review the signer yourself first. A release signed with a scheme `plugin.yaml` declares no signer for prints a warning but still installs; the `plugins:` write is safe either way, and you can add trust for it manually afterward.

`install` refuses to overwrite an already-declared plugin name, and only supports a single-file CUE `kevin.cue` - a YAML/JSON environment, or a CUE file using `package` mode (`plugins:` could live in any of several files sharing it), gets the same error `show` would have you work around by pasting the snippet in yourself.
