---
title: "MCP server"
description: "The MCP tools kevin exposes to a coding agent, and how they ride the console's own listener."
weight: 7
---

# MCP server

The MCP server gives a coding agent the same read/control surface the console gives a human, over [MCP](https://modelcontextprotocol.io)'s Streamable HTTP transport instead of SSE/htmx: `list_steps`, `get_step`, `rerun_step`, `export_step`, and `get_proxy_info`.

It mounts at `/_mcp` on the console's own HTTP router, rather than binding a listener of its own. A fourth loopback port for one more agent-facing surface would be one more thing to print, route through a firewall exception, and explain - the console already owns exactly the state an MCP tool call needs and already binds one HTTP server for exactly this session's lifetime, so the MCP server rides on it instead of duplicating that bind/listen/shutdown dance.

`get_proxy_info` reads the proxy's routing table and egress allow list directly, and `export_step` calls a step's plugin `Export` RPC through the session's already-running plugin connection - the same RPC a `commands:` entry's `needs` makes through `kevin do`, but against the live session instead of a freshly relaunched one, since an MCP client is asking about an environment that's already up. The console's own **MCP** tab shows the URL and the `claude mcp add` command to register it, the same page that shows the proxy's PAC URL and export line.

A plugin can contribute its own tools alongside these five, through the `CallTool` RPC. See [The plugin protocol]({{< relref "/docs/extending/plugin-protocol" >}}) for the wire method and [Writing a plugin]({{< relref "/docs/extending/writing-a-plugin" >}}) for `ToolProvider`.
