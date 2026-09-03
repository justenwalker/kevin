---
title: "Architecture"
description: "A map of kevin's parts and how they connect."
weight: 1
mermaid: true
---

# Architecture

A map of kevin's parts and how they connect. Each part has its own page below with the reasoning behind its shape.

```mermaid
graph TD
    CLI["kevin CLI"]
    ENG[engine]
    CFG["Config"]
    DAG["DAG engine"]
    HOST[pluginhost]
    CON[console]
    CA["CA"]
    MCPN[MCP server]
    PLUG[["plugin process"]]
    CRI["cri<br/>(eg: docker)"]
    PROXY[proxy]
    LOOP(["host loopback"])

    subgraph DOCKERNET["container network"]
        RELAY["relay<br/>(container)"]
        STEPC["step containers<br/>(container/kind)"]
    end

    CLI -->|invokes| ENG
    ENG -->|loads| CFG
    ENG -->|uses| DAG
    ENG -->|starts| HOST
    ENG -->|drives| CON
    ENG -->|creates| CA
    CON -->|mounts /_mcp| MCPN
    MCPN -->|tool calls, step ops| ENG
    HOST -.gRPC.-> PLUG
    PLUG -->|shells out| CRI
    ENG -->|network, GC| CRI
    STEPC -.->|publishes| LOOP
    STEPC -.->|resolves, forwards| RELAY
    RELAY -.->|socks5| LOOP
    PROXY -.->|dials| LOOP
    RELAY -->|forwards| PROXY
    ENG -->|starts| PROXY
    CA -->|signs leaf| PROXY
    CRI -.->|creates, attaches| RELAY
    CRI -.->|creates, attaches| STEPC
    RELAY -.->|socks5 dial| STEPC
    PROXY -->|traffic records| ENG

    classDef dashedNode stroke-dasharray: 5 5
    class STEPC dashedNode
```

| Part          | Responsibility                                                |
|---------------|---------------------------------------------------------------|
| CLI           | Parses the command line.                                      |
| Engine        | Loads the environment, starts the plugins, walks the DAG.     |
| Configuration | Reads `kevin.cue`. Validates every step before anything runs. |
| DAG engine    | Orders the steps. Runs independent steps concurrently.        |
| Plugin host   | Starts a plugin process and keeps the process alive.          |
| Plugin SDK    | The public API that a plugin author implements.               |
| Wire contract | The gRPC service between the engine and a plugin.             |
| Proxy         | Terminates TLS, routes to a workload, controls egress.        |
| Console       | Shows the DAG state, the logs, and the proxy traffic.         |
| MCP server    | Exposes the running session to an MCP client over Streamable HTTP, mounted at `/_mcp` on the console's own listener. |
| CA            | Creates the CA and mints a leaf certificate for the proxy.    |
| Relay         | In-network DNS + TLS/HTTP forwarder for name resolution with no host changes, plus a SOCKS5 gateway a host process can dial to reach a step directly. |
