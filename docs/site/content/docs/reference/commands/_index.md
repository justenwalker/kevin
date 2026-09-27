---
title: "Commands"
weight: 20
bookCollapseSection: true
description: "Every kevin command and its flags."
---

# Commands

Every `kevin` command accepts these flags:

| Flag | Type | Default | Description |
|:-----|:----:|:-------:|:------------|
| `--dir`, `-C` | `string` | `.` | project directory that holds a kevin environment file |
| `--env`, `-e` | `string` | `$KEVIN_ENV` | select a named environment instead of the default |
| `--tag`, `-t` | `[]string` | none | inject a CUE `@tag` value (repeatable); requires the environment file to declare a CUE package |
| `--engine` | `string` | `$KEVIN_ENGINE`, else auto-detected | container engine to use: `docker` or `podman` |
| `--debug` | `bool` | `false` | log at debug level |
