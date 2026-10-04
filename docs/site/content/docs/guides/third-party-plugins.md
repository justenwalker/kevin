---
title: "Third-party plugins"
description: "Find a plugin in an index, install it into kevin.cue, and trust its signer."
weight: 6
---

# Third-party plugins

A third-party plugin adds step types that kevin does not ship. You can find plugins in an index, a git repository that lists plugins and their releases.

## Add an index

```sh
kevin plugin index add https://github.com/acme/kevin-plugin-index
```

kevin clones the repository and prints the number of plugins it found. The index gets an alias derived from the URL. To choose the alias, add `--as <alias>`.

To see your indexes, run `kevin plugin index list`. To get new releases, run `kevin plugin index update`.

## Find a plugin

```sh
kevin plugin search postgres
```

Each result shows `<alias>/<name>`, a summary, and the latest version. With no query, `search` lists every plugin.

To see the versions of a plugin and the `plugins:` entry for the latest one:

```sh
kevin plugin index show postgres
```

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

If two indexes have a plugin with the same name, the command fails and lists the aliases. Use `<alias>/<name>`, for example `acme/postgres`.

## Install a plugin

```sh
kevin plugin index install postgres
```

```
trusted	0764f8e1f91b2058
installed	postgres	1.4.0
```

`install` does two things:

1. Adds the signers that the plugin's index entry declares to your trust store.
2. Adds the `plugins:` entry to your environment file.

To install an older release, add `--version 1.3.0`. The version must match exactly. To review the signer before you trust it, add `--no-trust`, then trust it yourself (see below).

`install` fails if the environment file already declares a plugin with that name, or if the file is package-mode CUE. In those cases, copy the entry from `kevin plugin index show` into the file yourself.

## Trust a signer yourself

Do this when you add a `plugins:` entry by hand, or used `--no-trust`.

For a `minisign` package, add the signer's public key file:

```sh
kevin plugin trust add ./signer.pub
```

For a `sigstore` package, add the identity and issuer from the plugin's `signing:` block:

```sh
kevin plugin trust add-identity \
  --identity ci@acme.example \
  --issuer https://token.actions.githubusercontent.com
```

Check the result with `kevin plugin trust list`.

## Fetch from a registry with a private CA

Do this when an `oci:` or `http:` source uses a certificate that your system does not trust.

1. Save the CA certificate as a PEM file.
2. Set `KEVIN_PLUGIN_CA_FILE` to that file:

   ```sh
   export KEVIN_PLUGIN_CA_FILE=$PWD/registry-ca.pem
   ```

kevin trusts the certificates in the file in addition to your system roots. This applies to `kevin run`, `kevin plugin push`, and any other command that fetches a package. It does not apply to `cosign` when it verifies a sigstore signature.

## Use the plugin

1. Add a step that uses a step type of the plugin:

   ```cue
   env: db: {
       uses: "postgres:server"
       with: {
           // fields from the plugin's documentation
       }
   }
   ```

2. Download the plugin and verify its signature:

   ```sh
   kevin init
   ```

3. Check the environment file against the plugin's schema:

   ```sh
   kevin validate
   ```

The plugin's documentation lists its step types and their `with` fields.

## Related

- [Environment file: plugins]({{< relref "/docs/reference/environment-file#plugins" >}})
- [`kevin plugin`]({{< relref "/docs/reference/commands/plugin" >}})
- [Plugin trust]({{< relref "/docs/concepts/plugin-trust" >}}): what an index does and does not guarantee.
