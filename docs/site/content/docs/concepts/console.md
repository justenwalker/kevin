---
title: "Console"
description: "How the console renders and streams updates with templ, htmx, and SSE."
weight: 6
---

# Console

The console renders HTML on the server with templ, and updates the page with htmx over server-sent events (SSE). The scripts are embedded in the `kevin` binary, so the console works with no network access.

## One stream per page

The page renders the current state, then opens one event stream. Each change arrives on that stream as an HTML fragment that names the element it replaces. A step row replaces itself with `hx-swap-oob`. A log line or a request row arrives in an `hx-partial` that names its region and how to insert it.

A [step group]({{< relref "/docs/reference/environment-file#step-groups" >}}) row is the exception. Only its header is replaced, because the row also holds the control that expands or collapses the group. Replacing the whole row would collapse an expanded group on every update.

htmx 4 removed `sse-swap`, which htmx 2 used to subscribe each element to its own event. One stream with fragments that name their targets replaces those subscriptions.

## Reconnects

When a client connects, the server sends a full repaint. htmx reconnects on its own after a dropped connection. Without the repaint, the page would show old state until the next change. The page shows a banner while the connection is down.

## Slow clients

The server never waits for a browser. Each client has a bounded buffer. A client that falls behind is disconnected, and reconnects with a full repaint.
