---
title: "⌨️ Commands"
weight: 20
bookCollapseSection: true
description: "Every kevin subcommand, generated from its cobra definition."
---

# Commands

Every `kevin` subcommand, generated from its cobra definition. Every
subcommand also accepts these global flags, defined once on the root
`kevin` command:

| Flag | Type | Default | Description |
|:-----|:----:|:-------:|:------------|
| `--dir`, `-C` | `string` | `.` | project directory that holds a kevin environment file |
| `--env`, `-e` | `string` | `$KEVIN_ENV` | select a named environment instead of the default |
| `--debug` | `bool` | `false` | log at debug level |
