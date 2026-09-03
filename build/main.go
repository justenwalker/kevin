//go:build gnob

// Command gnob is the build system of kevin.
//
// Build the binary one time:
//
//	go generate -C ./build -tags gnob .
//
// Then run a target from the repository root:
//
//	./build/gnob            # default target
//	./build/gnob -help      # list the targets
//	./build/gnob test
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

//go:generate go build -o gnob -tags gnob ./...

var (
	gnob     = GnobLib.Main
	makefile = GnobLib.Makefile
	cmd      = GnobLib.Cmd
	logger   = GnobLogger
)

// binaries are the commands that the build target compiles.
var binaries = []string{
	"./cmd/kevin",
	"./cmd/kevin-plugin-echo",
}

// protocPlugins are the code generators that buf runs. buf finds them on
// PATH.
var protocPlugins = []string{
	"google.golang.org/protobuf/cmd/protoc-gen-go",
	"google.golang.org/grpc/cmd/protoc-gen-go-grpc",
}

func main() {
	gnob.GoRebuildYourself("*.go")

	exe, err := os.Executable()
	if err != nil {
		logger.Error("[kevin:build] cannot locate the gnob binary", "error", err)
		os.Exit(1)
	}
	root := filepath.Dir(filepath.Dir(exe))
	if err = os.Chdir(root); err != nil {
		logger.Error("[kevin:build] cannot enter repository root", "error", err, "root", root)
		os.Exit(1)
	}

	mf := makefile.New(Default, Generate, Build, PackagePlugin, Test, Integration, Lint, Fmt, Tidy, Clean, E2E, RelayImage, Release, Docs, DocsServe, GHPages)
	mf.Run(context.Background())
}

// run starts a command and connects the output to the terminal.
func run(ctx context.Context, extra GnobExecOption, name string, args ...string) error {
	opts := []GnobExecOption{
		cmd.WithStdout(os.Stdout),
		cmd.WithStderr(os.Stderr),
	}
	if extra != nil {
		opts = append(opts, extra)
	}
	return cmd.ExecOpt(ctx, cmd.ExecOptions(opts...), name, args...).Run()
}

// goRun runs a go subcommand.
func goRun(ctx context.Context, args ...string) error {
	return run(ctx, nil, "go", args...)
}

// tool runs a pinned tool from tools.mod.
func tool(ctx context.Context, args ...string) error {
	return goRun(ctx, append([]string{"tool", "-modfile=tools.mod"}, args...)...)
}

var Default = GnobMakeTarget{
	Name:     "default",
	Desc:     "build everything",
	LongDesc: "Runs the build target.",
	Default:  true,
	Body:     makefile.DependOnly("build"),
}

var Generate = GnobMakeTarget{
	Name: "generate",
	Desc: "run code generators",
	LongDesc: "Regenerates protos/pb from the proto, the templ components from the\n" +
		"templ files, each builtin plugin's reference doc page from its\n" +
		"schema.cue and reference.md.tmpl, and each command's reference doc\n" +
		"page from its cobra definition and reference.md.tmpl.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		// buf finds protoc-gen-go and protoc-gen-go-grpc on PATH. Thus the
		// pinned versions must be on PATH.
		args := append([]string{"build", "-modfile=tools.mod", "-o", "bin/"}, protocPlugins...)
		if err := goRun(ctx, args...); err != nil {
			return err
		}

		bin, err := filepath.Abs("bin")
		if err != nil {
			return err
		}
		if err = run(ctx,
			cmd.WithEnvVars(map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH")}),
			"go", "tool", "-modfile=tools.mod", "buf", "generate"); err != nil {
			return err
		}

		if err = tool(ctx, "templ", "generate"); err != nil {
			return err
		}

		if err = goRun(ctx, "run", "./cmd/gen-reference-docs"); err != nil {
			return err
		}

		return goRun(ctx, "run", "./cmd/gen-command-docs")
	},
}

var Build = GnobMakeTarget{
	Name:     "build",
	Desc:     "build the binaries into bin/",
	LongDesc: "Builds kevin and every in-tree plugin into bin/.",
	Body: func(ctx context.Context, mf *GnobMakefile) error {
		if err := mf.Depend(ctx, "generate"); err != nil {
			return err
		}
		return goRun(ctx, append([]string{"build", "-o", "bin/"}, binaries...)...)
	},
}

var PackagePlugin = GnobMakeTarget{
	Name: "package-kevin-echo-plugin",
	Desc: "package kevin-plugin-echo as a file: source tar",
	LongDesc: "Packages bin/kevin-plugin-echo into bin/kevin-plugin-echo.tar.gz - a\n" +
		"worked example of the file: package format for a third-party plugin\n" +
		"author.",
	Body: func(ctx context.Context, mf *GnobMakefile) error {
		if err := mf.Depend(ctx, "build"); err != nil {
			return err
		}
		// kevin plugin pack tars its whole source directory, so the
		// entrypoint is staged alone rather than packing bin/ itself,
		// which holds every built binary.
		stageDir := "bin/pkg/echo"
		if err := stageFile("bin/kevin-plugin-echo", filepath.Join(stageDir, "kevin-plugin-echo")); err != nil {
			return err
		}
		return run(ctx, nil, "bin/kevin", "plugin", "pack", stageDir,
			"-o", "bin/kevin-plugin-echo.tar.gz",
			"--name", "echo",
			"--version", "v0.1.0",
			"--description", "Example Plugin 'echo'",
			"--entrypoint", "kevin-plugin-echo",
		)
	},
}

// stageFile copies src to dst, creating dst's parent directory as needed and
// giving dst mode 0o755.
func stageFile(src, dst string) (err error) {
	if mkdirErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkdirErr != nil {
		return fmt.Errorf("package-plugin: create %q: %w", filepath.Dir(dst), mkdirErr)
	}

	in, err := os.Open(src) //nolint:gosec // src is a build-script-controlled path, not user input
	if err != nil {
		return fmt.Errorf("package-plugin: open %q: %w", src, err)
	}
	defer in.Close() //nolint:errcheck // read-only handle, nothing to flush

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755) //nolint:gosec // dst is a build-script-controlled path, not user input; 0o755 gives the staged entrypoint its exec bit
	if err != nil {
		return fmt.Errorf("package-plugin: create %q: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("package-plugin: close %q: %w", dst, closeErr)
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("package-plugin: copy %q: %w", dst, err)
	}
	return nil
}

// RelayImageTag is the image that RelayImage builds.
const RelayImageTag = "kevin-relay:dev"

var RelayImage = GnobMakeTarget{
	Name: "relay-image",
	Desc: "build the kevin-relay docker image",
	LongDesc: "Cross-compiles kevin-relay for linux and the host architecture, then\n" +
		"builds " + RelayImageTag + " from build/relay.Dockerfile.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		dir, err := os.MkdirTemp("", "kevin-relay-image-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir) //nolint:errcheck // best effort cleanup of a temp directory

		bin := filepath.Join(dir, "linux", runtime.GOARCH, "kevin-relay")
		if err = os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			return err
		}
		buildEnv := cmd.WithEnvVars(map[string]string{
			"GOOS":        "linux",
			"GOARCH":      runtime.GOARCH,
			"CGO_ENABLED": "0",
		})
		if err = run(ctx, buildEnv, "go", "build", "-o", bin, "./cmd/kevin-relay"); err != nil {
			return err
		}

		return run(ctx, nil, "docker", "build",
			"-f", "build/relay.Dockerfile",
			"--build-arg", "TARGETARCH="+runtime.GOARCH,
			"-t", RelayImageTag, dir)
	},
}

// releaseVersionPattern matches a Go-module-compatible semver tag.
var releaseVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-.+)?$`)

// versionFile is embedded by internal/version, and read by both
// internal/cmd (kevin --version) and internal/relay (the default relay
// image tag), so it reads correctly from a module proxy or GitHub source
// archive too, not just a goreleaser build.
const versionFile = "internal/version/VERSION"

// docsVersionFile is a Hugo data file the docs site reads (via the
// {{% version %}} shortcode) to avoid hardcoding the version in prose.
const docsVersionFile = "docs/site/data/version.yaml"

var Release = GnobMakeTarget{
	Name: "release",
	Desc: "cut a version release with GoReleaser",
	LongDesc: "Usage: `./build/gnob release v1.2.3`. Tags, builds, and\n" +
		"publishes the release: cross-built kevin, the multi-arch\n" +
		"kevin-relay image on ghcr.io, and the GitHub release. Requires\n" +
		"GITHUB_TOKEN (repo + write:packages scope) exported and `docker\n" +
		"login ghcr.io` already done - gnob does neither for you.",
	Body: func(ctx context.Context, mf *GnobMakefile) error {
		args := mf.TargetArgs()
		if len(args) != 1 || !releaseVersionPattern.MatchString(args[0]) {
			return fmt.Errorf("release: usage: ./build/gnob release vX.Y.Z")
		}
		version := args[0]

		if os.Getenv("GITHUB_TOKEN") == "" {
			return fmt.Errorf("release: GITHUB_TOKEN is not set")
		}

		var status strings.Builder
		if err := cmd.ExecOpt(ctx, cmd.WithStdout(&status), "git", "status", "--porcelain").Run(); err != nil {
			return fmt.Errorf("release: git status: %w", err)
		}
		if strings.TrimSpace(status.String()) != "" {
			return fmt.Errorf("release: working tree is dirty, commit or stash first")
		}

		if cmd.ExecOpt(ctx, nil, "git", "rev-parse", "--verify", "--quiet", "refs/tags/"+version).Run() == nil {
			return fmt.Errorf("release: tag %s already exists", version)
		}

		if err := os.WriteFile(versionFile, []byte(version+"\n"), 0o644); err != nil {
			return fmt.Errorf("release: write %s: %w", versionFile, err)
		}
		docsVersion := fmt.Sprintf("version: %q\n", strings.TrimPrefix(version, "v"))
		if err := os.WriteFile(docsVersionFile, []byte(docsVersion), 0o644); err != nil {
			return fmt.Errorf("release: write %s: %w", docsVersionFile, err)
		}
		if err := run(ctx, nil, "git", "add", versionFile, docsVersionFile); err != nil {
			return fmt.Errorf("release: stage %s and %s: %w", versionFile, docsVersionFile, err)
		}
		if err := run(ctx, nil, "git", "commit", "-m", "Release "+version); err != nil {
			return fmt.Errorf("release: commit %s: %w", versionFile, err)
		}
		if err := run(ctx, nil, "git", "tag", "-a", version, "-m", "Release "+version); err != nil {
			return fmt.Errorf("release: create tag %s: %w", version, err)
		}

		if err := tool(ctx, "goreleaser", "release", "--clean", "--skip=publish"); err != nil {
			return fmt.Errorf("release: dry-run build failed, nothing pushed, tag %s left local-only: %w", version, err)
		}

		if err := run(ctx, nil, "git", "push", "origin", "HEAD"); err != nil {
			return fmt.Errorf("release: push the release commit: %w", err)
		}

		return tool(ctx, "goreleaser", "release", "--clean")
	},
}

const (
	// DocsDir is where the Hugo project and its markdown content live.
	DocsDir = "docs/site"

	// PagesDir is the persistent gh-pages worktree that Docs writes into.
	// Set up once with: git worktree add --orphan -b gh-pages gh-pages
	PagesDir = "gh-pages"
)

// buildHugoSite runs hugo against DocsDir, writing into PagesDir.
func buildHugoSite(ctx context.Context) error {
	return run(ctx, cmd.WithDir(DocsDir), "hugo",
		"--destination", "../../"+PagesDir, "--minify")
}

var DocsServe = GnobMakeTarget{
	Name: "docs-serve",
	Desc: "run the Hugo dev server for the documentation site",
	LongDesc: "Serves docs/site/ with live reload at http://localhost:1313/.\n" +
		"Requires the hugo binary on PATH, same as the docs target. Interrupt\n" +
		"to stop.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		logger.Info("[kevin:docs-serve] starting the Hugo dev server, interrupt to stop")
		return run(ctx, cmd.WithDir(DocsDir), "hugo", "server")
	},
}

var Docs = GnobMakeTarget{
	Name: "docs",
	Desc: "build the documentation site",
	LongDesc: "Builds the Hugo site under docs/site/ into the gh-pages/ worktree.\n" +
		"Requires the hugo binary on PATH - kevin does not pin a Hugo version,\n" +
		"the same way it does not pin docker - and requires the gh-pages/\n" +
		"worktree to already exist (git worktree add --orphan -b gh-pages gh-pages,\n" +
		"or run the gh-pages target instead, which sets that up). Docs only\n" +
		"writes files; committing and pushing gh-pages is a separate, manual\n" +
		"step - use the gh-pages target for that instead.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		if _, err := os.Stat(PagesDir); err != nil {
			return fmt.Errorf("docs: %s does not exist, set it up first with "+
				"`git worktree add --orphan -b gh-pages gh-pages`: %w", PagesDir, err)
		}
		return buildHugoSite(ctx)
	},
}

var GHPages = GnobMakeTarget{
	Name: "gh-pages",
	Desc: "rebuild gh-pages from scratch and commit it",
	LongDesc: "Wipes the gh-pages/ worktree and the gh-pages branch, recreates\n" +
		"both as a fresh orphan branch, builds the Hugo site into it, and\n" +
		"commits the result as that branch's only commit. gh-pages history\n" +
		"holds no information worth keeping - it's build output - so each run\n" +
		"replaces it outright rather than accumulating commits. Still does not\n" +
		"push: `git push --force origin gh-pages` is a separate, manual step.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		if err := os.RemoveAll(PagesDir); err != nil {
			return fmt.Errorf("gh-pages: remove %s: %w", PagesDir, err)
		}

		if err := run(ctx, nil, "git", "worktree", "prune"); err != nil {
			return fmt.Errorf("gh-pages: prune stale worktree metadata: %w", err)
		}

		if cmd.ExecOpt(ctx, nil, "git", "show-ref", "--verify", "--quiet", "refs/heads/gh-pages").Run() == nil {
			if err := run(ctx, nil, "git", "branch", "-D", "gh-pages"); err != nil {
				return fmt.Errorf("gh-pages: delete the existing branch: %w", err)
			}
		}
		if err := run(ctx, nil, "git", "worktree", "add", "--orphan", "-b", "gh-pages", PagesDir); err != nil {
			return fmt.Errorf("gh-pages: create the worktree: %w", err)
		}
		if err := buildHugoSite(ctx); err != nil {
			return err
		}
		if err := run(ctx, cmd.WithDir(PagesDir), "git", "add", "-A"); err != nil {
			return fmt.Errorf("gh-pages: stage the build output: %w", err)
		}
		if err := run(ctx, cmd.WithDir(PagesDir), "git", "commit", "-m", "Rebuild the documentation site"); err != nil {
			return fmt.Errorf("gh-pages: commit the build output: %w", err)
		}
		logger.Info("[kevin:gh-pages] rebuilt and committed; push when ready: git push --force origin gh-pages")
		return nil
	},
}

var Test = GnobMakeTarget{
	Name:     "test",
	Desc:     "run the tests",
	LongDesc: "Runs the full test suite with the race detector.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		return goRun(ctx, "test", "-race", "-cover", "./...")
	},
}

var Integration = GnobMakeTarget{
	Name: "integration",
	Desc: "run the integration test suites",
	LongDesc: "Runs the tests behind the integration build tag. These suites need\n" +
		"a running Docker daemon and take several minutes.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		return goRun(ctx, "test", "-tags", "integration", "-race", "-timeout", "900s", "./...")
	},
}

var Lint = GnobMakeTarget{
	Name:     "lint",
	Desc:     "run golangci-lint",
	LongDesc: "Runs golangci-lint with the repository configuration.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		return tool(ctx, "golangci-lint", "run", "./...")
	},
}

var Fmt = GnobMakeTarget{
	Name:     "fmt",
	Desc:     "format the code",
	LongDesc: "Applies gofumpt and orders imports with gci.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		if err := tool(ctx, "gci", "write",
			"-s", "standard", "-s", "default", "-s", "prefix(github.com/justenwalker/kevin)",
			"--skip-generated", "./"); err != nil {
			return err
		}
		return tool(ctx, "gofumpt", "-l", "-w", ".")
	},
}

var Tidy = GnobMakeTarget{
	Name: "tidy",
	Desc: "tidy the module",
	LongDesc: "Runs go mod tidy. tools.mod is not tidied: it holds only tool\n" +
		"directives, and is maintained with `go get -modfile=tools.mod -tool <pkg>`.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		return goRun(ctx, "mod", "tidy")
	},
}

var Clean = GnobMakeTarget{
	Name:     "clean",
	Desc:     "remove build output",
	LongDesc: "Removes bin/ and the environment state of the examples.",
	Body: func(_ context.Context, _ *GnobMakefile) error {
		for _, path := range []string{"bin", "examples/echo/.kevin", "examples/web/.kevin"} {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		}
		return nil
	},
}

var E2E = GnobMakeTarget{
	Name: "e2e",
	Desc: "run the end-to-end test suite",
	LongDesc: "Runs the tests behind the e2e build tag - the automatable\n" +
		"parts of docs/MANUAL_TESTING.md, driven through the real kevin\n" +
		"binary. Needs a running Docker daemon and takes several minutes.",
	Body: func(ctx context.Context, _ *GnobMakefile) error {
		return goRun(ctx, "test", "-tags", "e2e", "-race", "-v", "-timeout", "900s", "./tests/e2e/...")
	},
}
