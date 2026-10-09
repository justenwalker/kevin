---
title: "Contributing"
weight: 95
---

# Contributing

kevin builds with [gnob](https://github.com/justenwalker/gnob), vendored under `build/`. Bootstrap it once. After that, it rebuilds itself when its sources change:

```sh
go generate -C ./build -tags gnob .            # bootstrap, once
./build/gnob -help                             # list every target, with a one-line description each
./build/gnob build                             # build bin/kevin and bin/kevin-plugin-echo
./bin/kevin -C examples/web run                # try it, Ctrl-C to remove
```

Run one test with `go test`:

```sh
go test ./internal/dag/... -run TestName -v
go test -C tests/e2e -tags e2e -run TestName ./...    # an end-to-end test: tests/e2e is its own Go module
```

`golangci-lint` (`.golangci.yaml`) enables every linter, then disables some. The comments in the file give the reason for each. Read them before you add a `//nolint`. [`docs/GO_CONVENTIONS.md`](https://github.com/justenwalker/kevin/blob/main/docs/GO_CONVENTIONS.md) covers the style rules that the linter cannot check.

See [Architecture]({{< relref "/docs/concepts/architecture" >}}) for how the parts of kevin fit together.

## Tests

kevin has three kinds of test: unit, integration, and end-to-end. [ADR-0008](https://github.com/justenwalker/kevin/blob/main/docs/adr/0008-test-tiers-by-who-observes-the-behavior.md) describes them.

To choose where a new test goes, pick the first case that fits:

1. The test needs no daemon. Write a unit test next to the code, with fakes.
2. The test needs a real dependency such as Docker or a cluster, inspects what kevin left behind, or injects a failure. Write an integration test. Put it in a `*_integration_test.go` file behind the `integration` build tag, as [GO-017](https://github.com/justenwalker/kevin/blob/main/docs/GO_CONVENTIONS.md#go-017-an-integration-test-is-a-testify-suitesuite-gated-by-both-the-build-tag-and-a-runtime-skip) describes.
3. A user can see the behavior without knowing how kevin works, and no unit or integration test already proves it. Write an end-to-end test in `tests/e2e`, one suite for each user task, with the task named in its doc comment as [GO-020](https://github.com/justenwalker/kevin/blob/main/docs/GO_CONVENTIONS.md#go-020-an-integration-or-e2e-suites-doc-comment-ends-with-its-tier) describes.

Run each kind with its own target:

```sh
./build/gnob test           # unit tests, with the race detector
./build/gnob integration    # needs Docker
./build/gnob e2e            # needs Docker, drives the built kevin binary
./build/gnob coverage       # merge the three runs into coverage/coverage.html
```

The k3d and minikube integration suites need those binaries on `PATH`. An end-to-end suite skips when a tool it needs (`k3d`, `minikube`, `kubectl`, or Chrome) is missing.

## Changelog

kevin tracks release notes with [changie](https://changie.dev). Add an entry for each user-visible change:

```sh
go tool -modfile=tools.mod changie new
```

Pick a kind (Breaking, Added, Changed, or Fixed) and write one line for a reader of the release notes. The entry lands in `.changes/unreleased/`. Commit it with the change.

`./build/gnob release vX.Y.Z` batches the entries into `.changes/vX.Y.Z.md`, regenerates `CHANGELOG.md`, and uses that file as the GitHub release notes. The release fails when there are no entries. `CHANGELOG.md` is generated, so edit the entries instead.

## This site

This site is a [Hugo](https://gohugo.io) site in `docs/site/`, with the [hugo-book](https://github.com/alex-shpak/hugo-book) theme.

The pages under Reference > Steps and Reference > Commands are generated. Edit `internal/plugins/<type>/reference.md.tmpl` and the field comments in `schema.cue`, or `internal/cmd/reference/<command>.md.tmpl`, then run `./build/gnob generate`.

```sh
./build/gnob docs-serve     # live preview at http://localhost:1313/
./build/gnob docs-check     # build, then fail on links to a missing page or #anchor
./build/gnob docs           # build into the gh-pages/ worktree
```

`gh-pages/` is a git worktree of the orphan `gh-pages` branch. `./build/gnob gh-pages` creates it, or rebuilds it, and commits the site as the only commit on the branch. To publish, push the branch:

```sh
git push --force origin gh-pages
```
