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
