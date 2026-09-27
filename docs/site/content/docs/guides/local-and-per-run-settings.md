---
title: "Per-machine and per-run settings"
description: "Keep machine-specific values out of source control, and switch settings on the command line."
weight: 7
---

# Per-machine and per-run settings

Both tasks use CUE package mode: the environment file starts with a `package` clause, and kevin loads every `.cue` file in the directory with the same clause as one environment.

## Keep machine-specific values out of source control

1. Add a package clause to the top of `kevin.cue`:

   ```cue
   package kevin
   ```

2. Add a local file to `.gitignore`:

   ```sh
   echo 'kevin.local.cue' >> .gitignore
   ```

3. Put the machine-specific values in `kevin.local.cue`, with the same package clause:

   ```cue
   package kevin

   plugins: registry: config: token: "local-dev-token"
   ```

4. Check the result:

   ```sh
   kevin validate
   ```

kevin ignores a `.cue` file that does not have the same package clause, so check that `kevin.local.cue` has it.

## Switch a setting on the command line

1. Mark the field with a `@tag` attribute in a package-mode file:

   ```cue
   package kevin

   proxy: egress: deny: bool @tag(airgap,type=bool)
   ```

2. Pass the tag:

   ```sh
   kevin run -t airgap
   ```

`-t airgap` is the same as `-t airgap=true`. Use `-t name=value` for a string or number field.

## Use one switch for several fields

Declare the switch as its own field, then use it in an `if` block:

```cue
package kevin

airgap: bool | *false @tag(airgap,type=bool)

proxy: egress: deny: airgap
if airgap {
    domain: "airgap.kevin.home"
}
```

Use an `if` block to change a field that already has a default, such as `domain`. Do not replace the default with an expression, such as `domain: *myDomain | string`: kevin keeps the original default, `kevin.home`, and reports no error.

## Related

- [Environment file: tags]({{< relref "/docs/reference/environment-file#tags" >}})
- [Environment file: package mode]({{< relref "/docs/reference/environment-file#package-mode" >}})
- [Variables]({{< relref "/docs/guides/variables" >}}) - a `--var`/var-file/environment-variable alternative that needs no CUE package mode
