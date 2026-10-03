# kevin

Getting a local environment running usually means a page of setup steps, a pile of shell scripts, and leftovers to clean up when something fails. kevin replaces all of that with a single description of your environment. It starts every piece in the right order, makes your services reachable by name, and cleans up after itself when you are done.

**[Documentation](https://justenwalker.github.io/kevin/)** · **[Quickstart](https://justenwalker.github.io/kevin/docs/quickstart/)** · **[Examples](examples)**

## Features

- **Parallel startup in dependency order.** Steps with no dependency between them start at the same time. Each step gets the outputs of the steps it needs, such as an address or a kubeconfig path.
- **Clean teardown.** Ctrl-C or a failed step removes everything kevin started, even after a crash.
- **HTTPS names for your services.** A local proxy serves each service under a name such as `web.kevin.home`.
- **Egress control.** The proxy can block outbound traffic to hosts you did not allow.
- **Plugins.** Each step type is a plugin. You can write your own in any language that speaks gRPC.

## Try it

You need Docker or Podman running.

```sh
git clone https://github.com/justenwalker/kevin.git && cd kevin
go install github.com/justenwalker/kevin/cmd/kevin@latest
kevin -C examples/web run    # Ctrl-C to stop and remove everything
```

Prebuilt binaries are on the [releases page](https://github.com/justenwalker/kevin/releases).

## An environment file

```cue
project: "web-example"

proxy: {
	listen:       "127.0.0.1:18080"
	gateway_port: 18082
	egress: deny: true
}
console: listen: "127.0.0.1:18081"

env: {
	web: {
		uses: "builtin:container"
		with: {image: "nginx:alpine", expose: web: {port: 80}}
	}
	web_route: {
		uses:  "builtin:route"
		needs: ["web"]
		with: routes: [{host: "web", address: "${needs.web.out.host_80}"}]
	}
}
```

`kevin run` starts both steps and serves nginx at `https://web.kevin.home` through the proxy, which blocks every other outbound host. A web console shows each step and its logs while it runs.

## Learn more

- [Quickstart](https://justenwalker.github.io/kevin/docs/quickstart/): install kevin and run an example environment.
- [Guides](https://justenwalker.github.io/kevin/docs/guides/): Kubernetes clusters, proxy and egress, hostname interception, and more.
- [Environment file](https://justenwalker.github.io/kevin/docs/reference/environment-file/): write a `kevin.cue` for your own project.
- [kevin vs. other tools](https://justenwalker.github.io/kevin/docs/comparison/): Docker Compose, Tilt, Garden, and others.
- [Contributing](https://justenwalker.github.io/kevin/docs/contributing/): build kevin from source and run the tests.
