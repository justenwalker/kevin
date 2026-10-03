---
title: "Rerun on change"
description: "Rerun a step when files change while kevin run is running."
weight: 10
---

# Rerun on change

## Watch a directory

1. Add `watch` to the step. Each entry is a file or directory in the project directory:

   ```cue
   env: build: {
       uses:  "builtin:exec"
       watch: ["src/"]
       with: up: command: ["make", "build"]
   }
   ```

2. Start the environment:

   ```sh
   kevin run
   ```

3. Save a file under `src/`. kevin logs `change in src/main.go, rerunning` for the step and reruns it, along with the steps that depend on it.

## Validate the paths

Run `kevin validate`. It fails if a watched path does not exist, is absolute, or leaves the project directory. See the [`watch` field]({{< relref "/docs/reference/environment-file#steps" >}}).
