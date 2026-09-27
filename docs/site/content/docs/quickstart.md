---
title: "Quickstart"
weight: 1
---

# Quickstart

In this tutorial you install kevin, start an example environment with an nginx container, and reach it over HTTPS through the kevin proxy.

## Prerequisites

- Docker, running. Podman also works: add `--engine podman` to each `kevin` command.
- `git` and `curl`.
- A clone of the kevin repository, for the example environment:

  ```sh
  git clone https://github.com/justenwalker/kevin.git
  cd kevin
  ```

## Install kevin

Download the archive for your OS and architecture from the [GitHub releases page](https://github.com/justenwalker/kevin/releases), or use `curl`:

```sh
VERSION={{% version %}}
OS=darwin      # or: linux
ARCH=arm64     # or: amd64

curl -fsSL -o kevin.tar.gz \
  "https://github.com/justenwalker/kevin/releases/download/v${VERSION}/kevin_${VERSION}_${OS}_${ARCH}.tar.gz"
tar -xzf kevin.tar.gz kevin
sudo mv kevin /usr/local/bin/kevin
kevin --version
```

To verify the archive, download the release's `checksums.txt` and check it:

```sh
curl -fsSL -o checksums.txt \
  "https://github.com/justenwalker/kevin/releases/download/v${VERSION}/checksums.txt"
grep "kevin_${VERSION}_${OS}_${ARCH}.tar.gz" checksums.txt | shasum -a 256 -c -
```

## Check your machine

```sh
kevin -C examples/web doctor
```

Each check prints `ok`, `fail`, or `skip`. A `fail` on the container engine means Docker is not running. The CA checks report `not trusted` until you trust the kevin CA at the end of this tutorial. That is expected.

## Start the environment

```sh
kevin -C examples/web run
```

kevin prints the proxy and console addresses, then starts four steps from `examples/web/kevin.cue`: an nginx container named `web`, a route that serves it as `web.kevin.home`, and two containers that fetch the page from `web`. Wait until every step shows as ready. Leave it running.

## Reach the container through the proxy

In a second terminal, from the repository root:

```sh
curl --proxy http://127.0.0.1:18080 \
     --cacert ~/.kevin/root.crt \
     https://web.kevin.home/
```

The output is the nginx welcome page. The proxy terminated TLS with a certificate signed by the kevin CA, which `--cacert` tells `curl` to trust.

## Open the console

Open the console address that `kevin run` printed (`http://127.0.0.1:18081` for this example). The console shows the four steps, their logs, and the `curl` request you just sent.

## Stop the environment

Press Ctrl-C in the terminal that runs `kevin run`. kevin removes every container it started.

## Trust the CA

To use HTTPS without `--cacert`, add the kevin root CA to the trust stores of your machine. Do this once per machine:

```sh
kevin ca install
```

## Next steps

- [Environment file]({{< relref "/docs/reference/environment-file" >}}): write a `kevin.cue` for your own project.
- [Proxy and egress]({{< relref "/docs/guides/proxy-and-egress" >}}): route names to your services and control outbound traffic.
- [Kubernetes clusters]({{< relref "/docs/guides/kubernetes" >}}): add a local cluster to an environment.
- [Architecture]({{< relref "/docs/concepts/architecture" >}}): how the parts of kevin fit together.
