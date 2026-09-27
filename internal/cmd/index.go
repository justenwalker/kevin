package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/pkgtrust"
	"github.com/justenwalker/kevin/internal/pluginindex"
)

// pluginIndexCommand groups the subcommands that manage the local cache of
// git-cloned plugin index sources kevin plugin search reads from.
func pluginIndexCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Manage git repos that publish discoverable plugins",
	}
	cmd.AddCommand(
		pluginIndexAddCommand(opts),
		pluginIndexListCommand(opts),
		pluginIndexRemoveCommand(opts),
		pluginIndexUpdateCommand(opts),
		pluginIndexShowCommand(opts),
		pluginIndexInstallCommand(opts),
	)
	return cmd
}

func pluginIndexAddCommand(opts *options) *cobra.Command {
	var alias string
	c := &cobra.Command{
		Use:   "add <git-url>",
		Short: "Configure a git repo as a plugin index source",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			res, err := pluginindex.AddSource(cmd.Context(), args[0], alias)
			if err != nil {
				return err
			}
			return printSourceResult(cmd, res.Source, res.Plugins, res.Warnings)
		},
	}
	c.Flags().StringVar(&alias, "as", "", "alias for this source (default: derived from the URL)")
	return c
}

func pluginIndexListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured plugin index sources",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			sources, err := pluginindex.ListSources()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, s := range sources {
				if _, err := fmt.Fprintf(w, "%s\t%s\n", s.Alias, s.URL); err != nil {
					return fmt.Errorf("cmd: index list: %w", err)
				}
			}
			return nil
		},
	}
}

func pluginIndexRemoveCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <alias>",
		Short: "Remove a configured plugin index source",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.ran = true
			return pluginindex.RemoveSource(args[0])
		},
	}
}

func pluginIndexUpdateCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Re-clone every configured plugin index source",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			results, err := pluginindex.Update(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			var failed bool
			for _, r := range results {
				if r.Err != nil {
					failed = true
					if _, err := fmt.Fprintf(w, "error\t%s\t%s\n", r.Source.Alias, r.Err); err != nil {
						return fmt.Errorf("cmd: index update: %w", err)
					}
					continue
				}
				if err := printSourceResult(cmd, r.Source, r.Plugins, r.Warnings); err != nil {
					return err
				}
			}
			if failed {
				return ErrIndexUpdateFailed
			}
			return nil
		},
	}
}

func pluginIndexShowCommand(opts *options) *cobra.Command {
	var showVersion string
	c := &cobra.Command{
		Use:   "show <name-or-alias/name>",
		Short: "Show a plugin's metadata and versions, or one version's pasteable snippet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			catalog, err := pluginindex.LoadAll(cmd.Context())
			if err != nil {
				return err
			}
			p, err := catalog.Resolve(args[0])
			if err != nil {
				return err
			}
			if showVersion != "" {
				return printVersionSnippet(cmd, p, showVersion)
			}
			return printPluginShow(cmd, p)
		},
	}
	c.Flags().StringVar(&showVersion, "version", "", "show this exact version's snippet instead of the plugin's metadata and version list")
	return c
}

// resolvePluginVersion finds the exact version string in p.Versions, or
// p.Latest() when version is empty. It matches version exactly, with no
// fuzzy matching.
func resolvePluginVersion(p pluginindex.Plugin, version string) (pluginindex.Version, error) {
	if version == "" {
		v, ok := p.Latest()
		if !ok {
			return pluginindex.Version{}, fmt.Errorf("cmd: %s: %w", p.Name, pluginindex.ErrVersionNotFound)
		}
		return v, nil
	}
	for _, v := range p.Versions {
		if v.Version == version {
			return v, nil
		}
	}
	return pluginindex.Version{}, fmt.Errorf("cmd: %s@%s: %w", p.Name, version, pluginindex.ErrVersionNotFound)
}

// printVersionSnippet prints exactly one version's pasteable snippet.
func printVersionSnippet(cmd *cobra.Command, p pluginindex.Plugin, version string) error {
	v, err := resolvePluginVersion(p, version)
	if err != nil {
		return err
	}
	snippet, err := pluginindex.Snippet(v, p.Name)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", snippet); err != nil {
		return fmt.Errorf("cmd: index show: %w", err)
	}
	return nil
}

// printPluginShow prints p's metadata, every known version (newest first),
// and the latest version's pasteable snippet.
func printPluginShow(cmd *cobra.Command, p pluginindex.Plugin) error {
	w := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(w, "name\t%s\n", p.Name); err != nil {
		return fmt.Errorf("cmd: index show: %w", err)
	}
	if _, err := fmt.Fprintf(w, "summary\t%s\n", p.Summary); err != nil {
		return fmt.Errorf("cmd: index show: %w", err)
	}
	if p.Homepage != "" {
		if _, err := fmt.Fprintf(w, "homepage\t%s\n", p.Homepage); err != nil {
			return fmt.Errorf("cmd: index show: %w", err)
		}
	}
	if p.Maintainer != "" {
		if _, err := fmt.Fprintf(w, "maintainer\t%s\n", p.Maintainer); err != nil {
			return fmt.Errorf("cmd: index show: %w", err)
		}
	}
	if _, err := fmt.Fprintf(w, "repo\t%s (%s)\n", p.Repo.Alias, p.Repo.URL); err != nil {
		return fmt.Errorf("cmd: index show: %w", err)
	}

	latest, hasLatest := p.Latest()
	for _, v := range p.Versions {
		marker := ""
		if hasLatest && v.Version == latest.Version {
			marker = " (latest)"
		}
		if _, err := fmt.Fprintf(w, "version\t%s%s\n", v.Version, marker); err != nil {
			return fmt.Errorf("cmd: index show: %w", err)
		}
	}
	if !hasLatest {
		return nil
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return fmt.Errorf("cmd: index show: %w", err)
	}
	return printVersionSnippet(cmd, p, latest.Version)
}

// printSourceResult prints one source's alias, URL, and plugin count,
// followed by any warnings - the shared output shape "index add" and
// "index update" both print for a successfully synced source.
func printSourceResult(cmd *cobra.Command, src pluginindex.Source, plugins int, warnings []string) error {
	w := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(w, "ok\t%s\t%s\t%d plugins\n", src.Alias, src.URL, plugins); err != nil {
		return fmt.Errorf("cmd: index: %w", err)
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(w, "warning\t%s\t%s\n", src.Alias, warning); err != nil {
			return fmt.Errorf("cmd: index: %w", err)
		}
	}
	return nil
}

// pluginIndexInstallCommand resolves a plugin (optionally pinned to a
// version), trusts its declared signers, and writes its plugins: entry
// into the project's environment file - the copy/paste "show" leaves to
// the user, done in one step.
func pluginIndexInstallCommand(opts *options) *cobra.Command {
	var installVersion string
	var noTrust bool
	c := &cobra.Command{
		Use:   "install <name-or-alias/name>",
		Short: "Trust a plugin's signer and add it to the environment file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			catalog, err := pluginindex.LoadAll(cmd.Context())
			if err != nil {
				return err
			}
			p, err := catalog.Resolve(args[0])
			if err != nil {
				return err
			}
			v, err := resolvePluginVersion(p, installVersion)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			if !noTrust {
				if trustErr := trustVersion(w, p, v); trustErr != nil {
					return trustErr
				}
			}

			snippet, err := pluginindex.Snippet(v, p.Name)
			if err != nil {
				return err
			}
			if insertErr := config.InsertPlugin(opts.dir, opts.name, snippet); insertErr != nil {
				return insertErr
			}
			if _, err := fmt.Fprintf(w, "installed\t%s\t%s\n", p.Name, v.Version); err != nil {
				return fmt.Errorf("cmd: index install: %w", err)
			}
			return nil
		},
	}
	c.Flags().StringVar(&installVersion, "version", "", "install this exact version instead of the latest")
	c.Flags().BoolVar(&noTrust, "no-trust", false, "skip adding the signer to the local trust store")
	return c
}

// trustVersion trusts every entry in p.Signers whose scheme matches v's
// signing scheme, printing what was trusted. v unsigned is a no-op; v
// signed with no matching signer prints a warning instead of failing -
// installing the plugins: entry is still safe to do even without trust.
func trustVersion(w io.Writer, p pluginindex.Plugin, v pluginindex.Version) error {
	sig := v.Source.Signing
	if sig == nil {
		return nil
	}
	signers := p.SignersFor(sig.Scheme)
	if len(signers) == 0 {
		if _, err := fmt.Fprintf(w, "warning\tno %s signer declared for %s\n", sig.Scheme, p.Name); err != nil {
			return fmt.Errorf("cmd: index install: %w", err)
		}
		return nil
	}
	for _, s := range signers {
		id, err := trustSigner(s)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "trusted\t%s\n", id); err != nil {
			return fmt.Errorf("cmd: index install: %w", err)
		}
	}
	return nil
}

// trustSigner adds s to the appropriate local trust store and reports
// what it trusted, for display.
func trustSigner(s pluginindex.Signer) (string, error) {
	switch s.Scheme {
	case config.SigningSchemeMinisign:
		return pkgtrust.AddKeyText(s.Key)
	case config.SigningSchemeSigstore:
		if err := pkgtrust.AddIdentity(s.Identity, s.Issuer); err != nil {
			return "", err
		}
		return s.Identity + "/" + s.Issuer, nil
	default:
		return "", fmt.Errorf("cmd: index install: unknown signer scheme %q", s.Scheme)
	}
}

// pluginSearchCommand searches every configured index source's plugins by
// name and summary. It is a sibling of pluginIndexCommand, not nested under
// it - "index list" already names the builtin step-type listing.
func pluginSearchCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "search [query]",
		Short: "Search plugins found across every configured index source",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			catalog, err := pluginindex.LoadAll(cmd.Context())
			if err != nil {
				return err
			}
			plugins := catalog.List()
			if len(args) == 1 {
				plugins = catalog.Search(args[0])
			}
			w := cmd.OutOrStdout()
			for _, p := range plugins {
				version := ""
				if latest, ok := p.Latest(); ok {
					version = latest.Version
				}
				if _, err := fmt.Fprintf(w, "%s/%s\t%s\t%s\n", p.Repo.Alias, p.Name, p.Summary, version); err != nil {
					return fmt.Errorf("cmd: search: %w", err)
				}
			}
			return nil
		},
	}
}
