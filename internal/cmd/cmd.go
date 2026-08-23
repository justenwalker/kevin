// Package cmd implements the kevin command line.
package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/engine"
	"github.com/justenwalker/kevin/internal/logging"
	"github.com/justenwalker/kevin/internal/mcpserver"
	"github.com/justenwalker/kevin/internal/ocipkg"
	"github.com/justenwalker/kevin/internal/pluginpkg"
	"github.com/justenwalker/kevin/internal/plugins"
	"github.com/justenwalker/kevin/internal/version"
	"github.com/justenwalker/kevin/plugin"
	"github.com/justenwalker/kevin/protos/pb"
)

// Version holds the version string.
var Version = version.String

// CommandError reports a usage error, such as an unknown command or a bad
// flag. Cmd is the command to print the usage text of.
//
// A failure inside a command body is not a CommandError. It reaches the caller
// unwrapped.
type CommandError struct {
	Err error
	Cmd *cobra.Command
}

func (e *CommandError) Error() string { return e.Err.Error() }
func (e *CommandError) Unwrap() error { return e.Err }

// envNameVar overrides the --env flag's default when set, so a shell or a
// direnv-style project setup can pick a named environment without passing
// --env on every invocation.
const envNameVar = "KEVIN_ENV"

// options holds the flags that every subcommand shares.
type options struct {
	dir   string
	name  string
	debug bool

	// ran becomes true when a command body starts.
	ran bool
}

// Run runs the command line and returns the error. On a usage error the error
// is a [CommandError], and the caller prints the usage text.
func Run(ctx context.Context, args []string) error {
	opts := &options{}

	root := &cobra.Command{
		Use:           "kevin",
		Short:         "Run a local dev environment",
		Long:          "kevin creates a Docker-backed dev environment from a DAG of steps. A plugin implements each step.",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRun: func(*cobra.Command, []string) {
			level := slog.LevelInfo
			if opts.debug {
				level = slog.LevelDebug
			}
			color := isatty.IsTerminal(os.Stderr.Fd()) && os.Getenv("NO_COLOR") == ""
			handlers := []slog.Handler{logging.NewHuman(os.Stderr, level, color)}
			if f := openLogFile(opts.dir); f != nil {
				handlers = append(handlers, slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug}))
			}
			slog.SetDefault(slog.New(logging.Fanout(handlers...)))
		},
	}

	flags := root.PersistentFlags()
	flags.StringVarP(&opts.dir, "dir", "C", ".", "project directory that holds a kevin environment file ("+strings.Join(config.FileNames, ", ")+")")
	flags.StringVarP(&opts.name, "env", "e", os.Getenv(envNameVar),
		"select a named environment (<name>.kevin.<ext> or .<name>.kevin.<ext>) instead of the default; defaults to "+envNameVar+" if set")
	flags.BoolVar(&opts.debug, "debug", false, "log at debug level")

	root.AddCommand(
		runCommand(opts),
		setupCommand(opts),
		teardownCommand(opts),
		initCommand(opts),
		validateCommand(opts),
		pluginCommand(opts),
		connectCommand(opts),
	)

	root.SetArgs(args)

	cmd, err := root.ExecuteContextC(ctx)
	switch {
	case err == nil:
		return nil
	case opts.ran:
		// cobra reports a usage error and a command failure the same way.
		// opts.ran separates them. The RunE body already wrapped its own
		// error with its own context; wrapping again here would double it.
		return err //nolint:wrapcheck // deliberate passthrough, see comment above
	default:
		return &CommandError{Err: err, Cmd: cmd}
	}
}

// printEnvironmentInfo prints the console, proxy, and MCP addresses, how to
// route the caller's own shell through the proxy, and how to register the
// MCP server with a coding agent, once all three are listening.
func printEnvironmentInfo(w io.Writer, env *pb.Environment) {
	mcpURL := "http://" + env.GetConsoleAddr() + mcpserver.Path

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintf(w, "  console  http://%s\n", env.GetConsoleAddr())
	_, _ = fmt.Fprintf(w, "  proxy    http://%s\n", env.GetHttpProxyAddr())
	_, _ = fmt.Fprintf(w, "  mcp      %s\n", mcpURL)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  to route this shell through it:")
	_, _ = fmt.Fprintf(w, "    export HTTP_PROXY=http://%s HTTPS_PROXY=http://%s\n",
		env.GetHttpProxyAddr(), env.GetHttpProxyAddr())
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "  to register this with a coding agent:")
	_, _ = fmt.Fprintf(w, "    claude mcp add --transport http kevin %s\n", mcpURL)
	_, _ = fmt.Fprintln(w)
}

// openLogFile opens the project's debug log file for appending, creating
// engine.WorkspaceDir under dir if needed. It returns nil if either step
// fails, so a read-only or missing project directory just forgoes the file
// and keeps logging to the terminal.
func openLogFile(dir string) *os.File {
	workspace := filepath.Join(dir, engine.WorkspaceDir)
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(workspace, "kevin.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path is the project's own workspace file
	if err != nil {
		return nil
	}
	return f
}

func runCommand(opts *options) *cobra.Command {
	var keep, open bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create the environment and hold it until an interrupt",
		Long: "run creates the env steps in dependency order, then blocks. " +
			"On an interrupt, run removes the steps in reverse order.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			return engine.Run(cmd.Context(), engine.Options{
				Dir:   opts.dir,
				Name:  opts.name,
				Scope: config.ScopeEnv,
				Keep:  keep,
				Debug: opts.debug,
				Open:  open,
				OnEnvironment: func(env *pb.Environment) {
					printEnvironmentInfo(os.Stderr, env)
				},
			})
		},
	}
	cmd.Flags().BoolVar(&keep, "keep", false, "leave the environment in place on exit")
	cmd.Flags().BoolVar(&open, "open", false, "open the console in the default browser once it's listening")
	return cmd
}

func setupCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Create the setup steps, which persist across runs",
		Long: "setup runs the setup DAG. These steps outlive one run, such as the " +
			"installation of the kevin CA into the trust stores.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			return engine.Run(cmd.Context(), engine.Options{
				Dir:    opts.dir,
				Name:   opts.name,
				Scope:  config.ScopeSetup,
				Keep:   true,
				NoWait: true,
				Debug:  opts.debug,
			})
		},
	}
}

func teardownCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "teardown",
		Short: "Remove what setup installed",
		Long: "teardown runs the setup DAG in reverse, such as the removal of " +
			"the kevin CA from the trust stores.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			return engine.Teardown(cmd.Context(), engine.Options{Dir: opts.dir, Name: opts.name, Debug: opts.debug})
		},
	}
}

func initCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Download every plugin the environment file references",
		Long: "init resolves each plugins: entry a step actually uses, downloading and extracting " +
			"a file:/oci:/http: source and verifying its signature if signed: true is set. It " +
			"starts no plugin process and validates nothing against a schema.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			names, err := engine.FetchPlugins(cmd.Context(), opts.dir, opts.name)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, name := range names {
				if _, err := fmt.Fprintln(w, name); err != nil {
					return fmt.Errorf("cmd: init: %w", err)
				}
			}
			return nil
		},
	}
}

func validateCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Check the environment file without creating anything",
		Long: "validate loads the environment file, starts its declared plugins, and unifies " +
			"every step's with block against its plugin's schema: everything run and setup do " +
			"before touching Docker. It creates nothing and needs no Docker daemon.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			cfg, plugins, _, err := engine.LoadAndLaunch(cmd.Context(), opts.dir, opts.name)
			engine.CloseAll(plugins)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d setup step(s), %d env step(s)\n",
				cfg.Project, len(cfg.Setup), len(cfg.Env))
			if err != nil {
				return fmt.Errorf("cmd: validate: %w", err)
			}
			return nil
		},
	}
}

// pluginCommand groups the subcommands that work with a builtin plugin.
func pluginCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Work with the plugins that ship inside kevin",
	}
	cmd.AddCommand(pluginRunCommand(opts), pluginListCommand(opts), pluginPackCommand(opts), pluginPushCommand(opts), pluginTrustCommand(opts))
	return cmd
}

// pluginRunCommand serves one provider that ships inside kevin, over the
// plugin protocol. The supervisor starts this command as a subprocess. A
// user does not run it.
func pluginRunCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:    "run <name>",
		Short:  "Serve a provider over stdio",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.ran = true
			if args[0] != plugins.Name {
				return fmt.Errorf("%w: %q, available: %s", ErrUnknownPlugin, args[0], plugins.Name)
			}
			plugin.Serve(plugins.Provider())
			return nil
		},
	}
}

// pluginPackCommand builds a plugin package (a gzip-compressed tar) from a
// directory, for distribution via a kevin.cue plugins: entry's file: or
// oci: source.
func pluginPackCommand(opts *options) *cobra.Command {
	var (
		out         string
		overlay     pluginpkg.Manifest
		manifestArg []string
	)

	cmd := &cobra.Command{
		Use:   "pack <dir>",
		Short: "Build a plugin package (tar.gz) from a directory",
		Long: "pack builds a plugin package from dir: a manifest.json plus the entrypoint " +
			"binary and any files it needs alongside it. dir may already hold a " +
			"manifest.json; --name, --version, --author, --description, --entrypoint, and " +
			"--args override or fill in its fields, and must fully supply the manifest when " +
			"dir has none.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			overlay.Args = manifestArg
			manifest, err := pluginpkg.Pack(args[0], out, overlay)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s -> %s\n", manifest.Name, manifest.Version, out); err != nil {
				return fmt.Errorf("cmd: pack: %w", err)
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVarP(&out, "output", "o", "", "output tar.gz path")
	flags.StringVar(&overlay.Name, "name", "", "plugin name (overrides manifest.json)")
	flags.StringVar(&overlay.Version, "version", "", "plugin version (overrides manifest.json)")
	flags.StringVar(&overlay.Author, "author", "", "plugin author (overrides manifest.json)")
	flags.StringVar(&overlay.Description, "description", "", "plugin description (overrides manifest.json)")
	flags.StringVar(&overlay.Entrypoint, "entrypoint", "", "entrypoint path, relative to dir (overrides manifest.json)")
	flags.StringSliceVar(&manifestArg, "args", nil, "default arguments for the entrypoint (overrides manifest.json)")
	_ = cmd.MarkFlagRequired("output") // static flag name, cannot fail
	return cmd
}

// pluginPushCommand pushes a plugin package built by pack to an OCI
// registry, for distribution via a kevin.cue plugins: entry's oci: source.
func pluginPushCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "push <tar.gz> <oci-ref>",
		Short: "Push a plugin package to an OCI registry",
		Long: "push uploads the plugin package at tar.gz to oci-ref (e.g. " +
			"ghcr.io/acme/plugin:v1), reusing whatever credentials docker login already wrote. " +
			"If tar.gz.minisig exists (minisign -Sm tar.gz's default output), push uploads it too, " +
			"for consumers with signed: true and the signing key in their trust store " +
			"(see kevin plugin trust).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			digest, err := ocipkg.Push(cmd.Context(), args[1], args[0])
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s@%s\n", args[1], digest); err != nil {
				return fmt.Errorf("cmd: push: %w", err)
			}
			return pushSignatureIfPresent(cmd, args[1], args[0])
		},
	}
}

// pushSignatureIfPresent uploads tarPath's sibling minisig file
// (tarPath+".minisig", minisign's own default output name) as ref's
// detached signature. Signing stays opt-in: an absent sibling file prints a
// hint instead of failing the push.
func pushSignatureIfPresent(cmd *cobra.Command, ref, tarPath string) error {
	sigPath := tarPath + ".minisig"
	if _, err := os.Stat(sigPath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cmd: push: stat %q: %w", sigPath, err)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(),
			"no %s found - sign with \"minisign -Sm %s\" to publish a signature\n", sigPath, tarPath)
		if err != nil {
			return fmt.Errorf("cmd: push: %w", err)
		}
		return nil
	}
	digest, err := ocipkg.PushSignature(cmd.Context(), ref, sigPath)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s@%s (signature)\n", ref, digest); err != nil {
		return fmt.Errorf("cmd: push: %w", err)
	}
	return nil
}

// pluginListCommand prints the qualified name of every step type that ships
// inside kevin, one per line. A step names a plugin this way in its own
// with block.
func pluginListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the step types that ship inside kevin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			w := cmd.OutOrStdout()
			for _, name := range plugins.Names() {
				if _, err := fmt.Fprintln(w, plugins.Name+":"+name); err != nil {
					return fmt.Errorf("cmd: list plugins: %w", err)
				}
			}
			return nil
		},
	}
}
