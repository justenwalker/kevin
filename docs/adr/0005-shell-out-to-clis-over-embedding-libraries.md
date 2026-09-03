# ADR-0005: Shell out to CLIs over embedding their libraries

**Status:** Accepted

## Context

kevin talks to Docker and to Kubernetes tooling (`kind`, `kubectl`, `helm`).
For each, a Go library exists that could be imported directly:
`github.com/docker/docker`'s client, or `sigs.k8s.io/kind` as a library
instead of shelling out to the `kind` binary. Both were considered and
rejected, for different but related reasons.

## Decision

Talk to Docker and to Kubernetes tooling by shelling out to their CLIs and
parsing the output (JSON where the CLI supports it), not by importing their
Go client libraries. The one deliberate exception is OCI registry access
(`internal/ocipkg`), which imports `cuelabs.dev/go/oci/ociregistry` rather
than shelling out to `docker pull` - see Consequences for why that one
doesn't violate the rule.

## Why

`github.com/docker/docker`'s client pulls in a dependency tree kevin
otherwise has no reason to carry, for functionality the `docker` CLI itself
already exposes over stable, versioned output formats. `sigs.k8s.io/kind`
as a library has a sharper problem, not just size: it reads
`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` from its own process's environment,
with no way to pass proxy configuration in otherwise. kevin needs those
variables scoped to each `kind` child process's own `exec.Cmd.Env`, not to
kevin's own process environment (which other steps and the engine itself
also read) - shelling out is the only way to get that isolation.

**DO** (`internal/docker/docker.go:442`):
```go
// run calls the docker binary and returns the standard output.
// A nil stdin gives the command no standard input.
func run(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	...
}
```
`internal/plugins/kind`, `internal/kubectlcmd`, and `internal/helmcmd` shell
out to their respective binaries the same way, each scoping proxy
environment variables to that one `exec.Cmd`.

**DO NOT:**
```go
import "github.com/docker/docker/client"

func Remove(ctx context.Context, name string) error {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		return err
	}
	return cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true})
}
```
Pulls in the full Docker SDK dependency tree for what `docker rm --force`
already does, and (for the `kind`-library equivalent) reads proxy
environment from kevin's own process rather than a scoped child process.

## Consequences

Every Docker/Kubernetes-facing package parses CLI text or JSON output
instead of getting typed structs from a client library, and has to track
CLI output-format stability itself. `internal/ocipkg`'s exception holds
because it costs no new dependency tree - `cuelabs.dev/go/oci/ociregistry`
is already resolved transitively via `cuelang.org/go`'s own OCI-registry
support - and a registry pull is a plain HTTP API, not a container-runtime
SDK with the same proxy-environment problem `kind`-as-a-library has.
