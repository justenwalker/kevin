# ADR-0007: Shell out to cosign for sigstore verification

**Status:** Accepted

## Context

A `signing: scheme: "sigstore"` plugin package (see [Signing
packages](../site/content/docs/environment-file.md)) needs its detached
bundle checked against sigstore's Fulcio-issued certificate and Rekor
transparency log. `sigstore-go` and `cosign` both ship this as an
importable Go library, alongside the `cosign` CLI that wraps the same
functionality. Both the library and the CLI were considered.

## Decision

Verify a sigstore bundle by shelling out to the `cosign` CLI
(`cosign verify-blob --bundle ...`), not by importing `sigstore-go` or
`cosign`'s own Go libraries. New package `internal/sigstorepkg` shells out
the same way `internal/docker`, `internal/kubectlcmd`, and `internal/helmcmd`
already do for their own external tools (see ADR-0005). Signing itself stays
a manual step the user runs with the `cosign` CLI directly - `kevin plugin
push` only detects and uploads whatever signature file is already there
(see [`internal/cmd/cmd.go`](../../internal/cmd/cmd.go)'s
`pushSignatureIfPresent`) - extending the same principle that keeps
minisign key material out of kevin's own binary to cover OIDC tokens and
Fulcio-issued certificates too.

## Why

ADR-0005 already rejects embedding Docker's and `kind`'s Go libraries for
pulling in a dependency tree kevin has no other reason to carry, with one
narrow exception: `internal/ocipkg`'s import of `cuelabs.dev/go/oci/ociregistry`,
justified because that module costs no *new* dependency tree - it's already
resolved transitively via `cuelang.org/go`'s own OCI-registry support - and
a registry pull is a plain HTTP API. `sigstore-go`/`cosign` as a library
does not clear that same bar: it pulls in Fulcio, Rekor, TUF, and in-toto
clients that nothing else in kevin's dependency graph already needs, and
Fulcio/Rekor is a purpose-built protocol, not a plain HTTP API a generic
client already covers.

**DO** (`internal/sigstorepkg/sigstorepkg.go:32`):
```go
func VerifyBlob(ctx context.Context, pkgPath, bundlePath, identity, issuer string) error {
	if _, err := LookPath(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, Binary, verifyBlobArgs(pkgPath, bundlePath, identity, issuer)...)
	...
}
```

**DO NOT:**
```go
import "github.com/sigstore/sigstore-go/pkg/verify"

func VerifyBlob(ctx context.Context, pkgPath, bundlePath, identity, issuer string) error {
	bundle, err := bundle.LoadJSONFromPath(bundlePath)
	// ...pulls in sigstore-go's Fulcio/Rekor/TUF client stack for a call
	// the cosign CLI already exposes over a stable command-line interface.
}
```

## Consequences

`cosign verify-blob` has no stable machine-readable failure taxonomy, so
every verification failure - identity mismatch, issuer mismatch, a bad
certificate chain, a bad Rekor proof - collapses into one
`sigstorepkg.ErrVerifyFailed` sentinel with cosign's raw stderr attached,
an honest precision regression from minisign's distinct
`ErrUnknownKeyID`/`ErrSignatureInvalid` sentinels. `cosign` becomes a new
opt-in external-tool dependency (`sigstorepkg.ErrCosignNotFound`), required
only for a user who actually sets `signing: scheme: "sigstore"` - minisign
stays the zero-dependency default.
