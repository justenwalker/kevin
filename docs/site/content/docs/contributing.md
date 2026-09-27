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
```

`golangci-lint` (`.golangci.yaml`) enables every linter, then disables some. The comments in the file give the reason for each. Read them before you add a `//nolint`. [`docs/GO_CONVENTIONS.md`](https://github.com/justenwalker/kevin/blob/main/docs/GO_CONVENTIONS.md) covers the style rules that the linter cannot check.

See [Architecture]({{< relref "/docs/concepts/architecture" >}}) for how the parts of kevin fit together.

## This site

This site is a [Hugo](https://gohugo.io) site in `docs/site/`, with the [hugo-book](https://github.com/alex-shpak/hugo-book) theme.

The pages under Reference > Steps and Reference > Commands are generated. Edit `internal/plugins/<type>/reference.md.tmpl` and the field comments in `schema.cue`, or `internal/cmd/reference/<command>.md.tmpl`, then run `./build/gnob generate`.

```sh
./build/gnob docs-serve     # live preview at http://localhost:1313/
./build/gnob docs           # build into the gh-pages/ worktree
```

`gh-pages/` is a git worktree of the orphan `gh-pages` branch. `./build/gnob gh-pages` creates it, or rebuilds it, and commits the site as the only commit on the branch. To publish, push the branch:

```sh
git push --force origin gh-pages
```
