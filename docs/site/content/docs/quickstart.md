---
title: "🧳 Quickstart"
weight: 1
---

# Quickstart

## Prerequisites

- `kevin` on your `PATH` (see below), or build it yourself (see [Contributing]({{< relref "/docs/contributing" >}}))
- a running Docker daemon
- a clone of this repository, for the example environments below

## Install

Download the archive for your OS/arch from the [GitHub releases page](https://github.com/justenwalker/kevin/releases), or with `curl`:

```sh
VERSION=0.0.1  # see the releases page for the latest
OS=darwin      # or: linux
ARCH=arm64     # or: amd64

curl -fsSL -o kevin.tar.gz \
  "https://github.com/justenwalker/kevin/releases/download/v${VERSION}/kevin_${VERSION}_${OS}_${ARCH}.tar.gz"
tar -xzf kevin.tar.gz kevin
sudo mv kevin /usr/local/bin/kevin
kevin --version
```

Each release also publishes a `checksums.txt` you can check the archive against:

```sh
curl -fsSL -o checksums.txt \
  "https://github.com/justenwalker/kevin/releases/download/v${VERSION}/checksums.txt"
grep "kevin_${VERSION}_${OS}_${ARCH}.tar.gz" checksums.txt | shasum -a 256 -c -
```

## First run

```sh
kevin -C examples/web run      # Ctrl-C to remove
```

`examples/web` starts an nginx container, then a second container that fetches the page from it by step name over the shared docker network. The proxy serves the container over TLS with a certificate that the kevin CA signs:

```sh
curl --proxy http://127.0.0.1:18080 \
     --cacert examples/web/.kevin/root.crt \
     https://web.kevin.home/
```

A [`builtin:route`]({{< relref "/docs/reference/route" >}}) step puts a name on the environment domain. The domain defaults to `kevin.home`. It works the same way whether the address behind the name is a container's published port (as in `examples/web`) or something reached through a relay, such as a Service inside a `builtin:kind` cluster.

A browser needs no configuration beyond pointing at the proxy's auto-config file, `http://127.0.0.1:18080/proxy.pac`: it sends the environment domain through the proxy and everything else direct. Behind the scenes, an in-network relay container resolves `<step>.<domain>` and egress denies by default: see [Name resolution]({{< relref "guides/relay-and-name-resolution" >}}) and [Proxy and egress]({{< relref "guides/proxy-and-egress" >}}) for the full model.

## Console

The console shows the step DAG, the logs of every step, and the traffic through the proxy, at the address that `kevin run` prints. Add `--open` to launch it in the default browser once it's listening.

## MCP server

`kevin run` also prints an `mcp` address - an MCP server at `/_mcp` on the console's own address, giving a coding agent the same step list/status, rerun, and proxy-info surface the console gives a human. Its own **MCP** tab on the console page has the exact command to register it with Claude Code (`claude mcp add --transport http kevin <url>`).

## Terminal output

Running in an actual terminal, `kevin run`/`teardown` draw a live, redrawing list instead of scrolling a line per event: one row per step, its state, and a progress bar once an estimate exists for it. Piped output, a log file, or `--debug` fall back to the plain line-per-event stream instead.

`kevin` also writes a full JSON copy of every log line, including debug-level lines, to `.kevin/kevin.log` in the project directory.

## Trust the CA

To drop the `--cacert` flag, install the kevin root into the trust stores of the machine, once, for every project:

```sh
kevin -C examples/trust setup       # install
kevin -C examples/trust teardown    # remove
```

See [CA and trust store]({{< relref "guides/ca-and-trust" >}}) for what this installs and why it's safe to run once for the machine rather than once per project.

## Next steps

- [Guides]({{< relref "guides" >}}): how to use each part of kevin, including the other three example environments in the repository.
- [Concepts]({{< relref "/docs/concepts" >}}): why the DAG engine, the proxy, and the CA are shaped the way they are.
- [Environment file]({{< relref "configuring-an-environment" >}}): the full `kevin.cue` shape.
