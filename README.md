# kevin

Kevin is a local development environment engine. It makes it possible to run complex local setups with a DAG of steps, a real network boundary, and an extensible plugin system.

**[Quickstart](docs/site/content/docs/quickstart.md)** &mdash; **[Docs](https://justenwalker.github.io/kevin/)** &mdash; **[Examples](examples)**

## Welcome to kevin!

- **A DAG, not a script.** Steps declare what they `need`; kevin figures out the order, runs independent steps in parallel.
- **Extensible plugin system.** Every step is provided by a plugin. All plugins speak the same gRPC protocol: there are no major differences between builtin and third-party plugins.
- **A real network boundary.** An HTTP/HTTPS proxy with TLS termination and an egress gateway. You can configure the proxy to only allow egress to specific hosts. You can also intercept and rewrite requests to services you control.

## Getting Started

Download a prebuilt binary from the [releases page](https://github.com/justenwalker/kevin/releases), or:

```sh
go install github.com/justenwalker/kevin/cmd/kevin@latest
kevin -C examples/web run    # Ctrl-C to remove
```

Or build from source with [gnob](https://github.com/justenwalker/gnob):

```sh
go generate -C ./build -tags gnob .    # bootstrap, once
./build/gnob                           # build into bin/
./bin/kevin -C examples/web run
```

See the [Quickstart](docs/site/content/docs/quickstart.md) for a walkthrough.

## Docs

Full documentation, including the environment file reference and plugin protocol, lives at <https://justenwalker.github.io/kevin/>.

## Usage Overview

An environment is one `kevin.cue` file: a map of steps, each naming a plugin step type and its `needs`:

```cue
project: "web-example"

env: {
	web: {
		uses:  "builtin:container"
		with: {image: "nginx:alpine", expose: [{port: 80}]}
	}
	web_route: {
		uses:  "builtin:route"
		needs: ["web"]
		with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
	}
}
```

```sh
kevin run              # bring the DAG up, tear it down on Ctrl-C
kevin setup            # bring up steps that should persist across runs (e.g. CA trust)
kevin teardown         # remove them again
```

While an environment is running, a web console shows the DAG and its logs live, and an MCP server (mounted at `/_mcp` on the console) lets a coding agent drive the same environment.

## How kevin Works

You write a `kevin.cue` file describing the pieces your local environment needs and how they depend on each other. kevin then brings the pieces up in dependency order, passing each step's output (an address, a kubeconfig path) to the steps that need it. There's no state file to get out of sync: tearing the environment down works from what's actually running, so it's safe even after a crash mid-run.

See [Architecture](docs/site/content/docs/concepts/architecture.md) for the full rationale.

## Plugins

Every step type - builtin or third-party - speaks the same gRPC protocol, documented in [Writing a Plugin](docs/site/content/docs/extending/writing-a-plugin.md). `kevin plugin list` prints every builtin step type; a project declares its own plugin binary under `plugins:` in `kevin.cue`.
