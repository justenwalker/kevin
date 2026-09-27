---
title: "kevin"
type: docs
---

# kevin

kevin starts a local development environment from one file, `kevin.cue`, and removes it when you stop. The file lists the pieces your environment needs (containers, Kubernetes clusters, manifests, commands) and how they depend on each other.

kevin gives you:

- **Parallel startup in dependency order.** Steps with no dependency between them start at the same time. Each step gets the outputs of the steps it needs, such as an address or a kubeconfig path.
- **Clean teardown.** If a step fails, or you press Ctrl-C, kevin removes everything it started. Teardown uses the resources that are running, not a state file, so it works after a crash.
- **HTTPS names for your services.** A forward proxy gives each service a name such as `web.kevin.home` and terminates TLS with a local CA. You do not edit `/etc/hosts`.
- **Egress control.** The proxy can block outbound traffic to hosts you did not allow, and logs each request in a web console.
- **Hostname interception.** A route can send traffic for a real hostname, such as `s3.amazonaws.com`, to a local container, with no change to your code.
- **Plugins.** Each step type is a plugin. You can write a new step type in any language that speaks gRPC.

## Start here

- [Quickstart]({{< relref "docs/quickstart" >}}): install kevin and run an example environment.
- [Guides]({{< relref "docs/guides" >}}): steps for specific tasks.
- [Reference]({{< relref "docs/reference" >}}): the environment file, commands, and every builtin step type.
- [Concepts]({{< relref "docs/concepts" >}}): how kevin works and why.
- [Extending kevin]({{< relref "docs/extending" >}}): write and publish a plugin.
- [kevin vs. other tools]({{< relref "docs/comparison" >}}): how kevin compares to Docker Compose, Terraform, Tilt, Garden, and shell scripts.

Source: [github.com/justenwalker/kevin](https://github.com/justenwalker/kevin)
