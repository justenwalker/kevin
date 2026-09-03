---
title: "Console"
description: "How the console renders and streams updates with templ, htmx, and SSE."
weight: 6
---

# Console

The console renders with templ and updates with htmx over SSE. Both scripts ship embedded in the binary, thus the console needs no network.

The page renders the state that the server holds, then opens **one** stream. Every later change arrives on that stream as a fragment that names its own target: a step row replaces itself with `hx-swap-oob`, and a log line or a request row arrives inside an `hx-partial` that names the region and the swap.

htmx 4 removed `sse-swap`. An unnamed message swaps into the element that opened the connection, and a named event dispatches a DOM event instead. One connection carrying out of band fragments therefore replaces the per-element subscriptions of htmx 2.

A client receives a full repaint as soon as it connects. htmx reconnects on its own after a drop, and without the repaint a browser would hold stale rows until the next change, which may never come.

The server never blocks on a browser. Each client has a bounded buffer, and a client that falls behind is dropped and reconnects.
