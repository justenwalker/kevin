---
title: "MCP server"
description: "The MCP tools kevin gives a coding agent, and why they use the console's address."
weight: 7
---

# MCP server

The MCP server gives a coding agent the same view and controls that the console gives a person. It uses [MCP](https://modelcontextprotocol.io)'s Streamable HTTP transport.

| Tool | Does |
|:-----|:-----|
| `list_steps` | Lists steps and their states. |
| `get_step` | Returns a step's state and logs. |
| `rerun_step` | Runs a step again. |
| `export_step` | Returns a step's exported values. |
| `get_proxy_info` | Returns the proxy address, its routes, and the egress allow list. |

A plugin can add tools for its step types. See [Writing a plugin]({{< relref "/docs/extending/writing-a-plugin#add-mcp-tools" >}}).

The **MCP** tab of the console shows the server URL and the `claude mcp add` command that registers it with Claude Code.

## One address with the console

The server is at `/_mcp` on the console's address, not on a port of its own. The console already has the state that the tools need, and already runs an HTTP server for the life of the environment. A separate port would be one more address to configure and print.

## Logs

`get_step` reads logs from the log file, not from the console's in-memory buffer, so it returns the full history of a step. Each call returns a `cursor`. Pass it as `since` on the next call to get only newer lines.

## Export

`export_step` calls the step's `Export` through the running plugin. `kevin do` makes the same call, but starts the plugins itself, because it runs outside the environment.
