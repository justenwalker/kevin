---
title: "Variables"
description: "Supply a value from outside kevin.cue, and mark it sensitive."
weight: 9
---

# Variables

## Declare a variable

1. Add a `variables` block to `kevin.cue`:

   ```cue
   variables: region: default: "us-east-1"
   ```

   Omit `default` to make the variable required.

2. Read it in a `with` block:

   ```cue
   env: app: {
       uses: "builtin:container"
       with: env: REGION: "${vars.region}"
   }
   ```

3. Check the result:

   ```sh
   kevin validate
   ```

## Declare a typed variable

1. Add a `type` constraint alongside (or instead of) `default`. See the [`#Variable` field table]({{< relref "/docs/reference/environment-file#variables" >}}) for the CUE expressions `type` accepts:

   ```cue
   variables: {
       replicas: {type: int & >=1 & <=10, default: 3}
       env_name: {type: "prod" | "staging" | "dev", default: "dev"}
       strict:   {type: bool}
   }
   ```

   A variable with no `type` is a plain string.

2. Read it the same way, in any field the plugin's own schema types to match:

   ```cue
   env: app: {
       uses: "builtin:container"
       with: replicas: "${vars.replicas}"
   }
   ```

3. Check the result:

   ```sh
   kevin validate
   ```

   A value outside the declared constraint fails validation with the constraint it violated.

## Use a variable in a plugin's config block

1. Read `${vars.<name>}` in a `plugins.<name>.config` block, the same way as a `with` block:

   ```cue
   variables: registry_size: {type: int, default: 2}
   plugins: registry: {
       cmd: "kevin-plugin-registry"
       config: replicas: "${vars.registry_size}"
   }
   ```

2. Check the result:

   ```sh
   kevin validate
   ```

## Supply a value

Use one of these, highest precedence first:

```sh
kevin run --var region=eu-west-1
KEVIN_VAR_REGION=eu-west-1 kevin run
kevin run --var-file secrets.env
```

A var-file holds one `KEY=VALUE` per line:

```sh
cat > secrets.env <<'EOF'
# comments and blank lines are skipped
region=eu-west-1
EOF
```

A typed variable parses the supplied value as CUE syntax rather than taking it literally - see the [`#Variable` field table]({{< relref "/docs/reference/environment-file#variables" >}}) for how each source is parsed.

## Mark a variable sensitive

1. Add `sensitive: true` to the variable:

   ```cue
   variables: api_key: sensitive: true
   ```

2. Supply the value the same way as any other variable:

   ```sh
   kevin run --var api_key=sk-123
   ```

A field that reads `${vars.api_key}` shows as redacted in the console's Inputs tab and in `get_step`'s MCP output, whether the field also carries `@sensitive()` in its plugin's schema or not.

## Related

- [Environment file: variables]({{< relref "/docs/reference/environment-file#variables" >}})
- [CEL expressions: vars]({{< relref "/docs/reference/cel-expressions#vars" >}})
- [Per-machine and per-run settings]({{< relref "/docs/guides/local-and-per-run-settings" >}}) - the CUE `@tag` alternative, for a package-mode environment file
