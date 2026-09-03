---
title: "Contributing"
weight: 95
---

# Contributing

Build orchestration is [gnob](https://github.com/justenwalker/gnob), vendored under `build/`. Bootstrap once, then it self-rebuilds when its own sources change:

```sh
go generate -C ./build -tags gnob .            # bootstrap, once
./build/gnob -help                             # list every target, with a one-line description each
./build/gnob build                             # build bin/kevin and bin/kevin-plugin-echo
./bin/kevin -C examples/web run                # try it, Ctrl-C to remove
```

Run a single test with `go test`'s own flags, same as any Go package:

```sh
go test ./internal/dag/... -run TestName -v
```

`golangci-lint` (`.golangci.yaml`) enables every linter and disables specific ones deliberately: read the config's comments before adding a `//nolint`, the exclusion you need probably already exists. [`docs/GO_CONVENTIONS.md`](https://github.com/justenwalker/kevin/blob/main/docs/GO_CONVENTIONS.md) covers the house style the linter can't check.

For *why* kevin is shaped the way it is, see [Architecture]({{< relref "/docs/concepts/architecture" >}}).

## This site

This site is a [Hugo](https://gohugo.io) site under `docs/site/`, using the [hugo-book](https://github.com/alex-shpak/hugo-book) theme via Hugo Modules.

```sh
./build/gnob docs-serve     # live preview at http://localhost:1313/
./build/gnob docs           # build into the gh-pages/ worktree
```

`gh-pages/` is a persistent git worktree checked out to an orphan `gh-pages` branch. `./build/gnob gh-pages` sets it up (or rebuilds it from scratch) and commits the result as that branch's only commit; `gh-pages` history holds no information worth keeping, it's build output. Either way, pushing the branch is a separate, manual step:

```sh
git push --force origin gh-pages
```
