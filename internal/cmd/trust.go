package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// pluginTrustCommand groups the subcommands that manage the local trust
// stores a plugins.<name> entry's signing block verifies against:
// ~/.kevin/trusted-keys for signing: scheme: "minisign", and
// ~/.kevin/trusted-identities for signing: scheme: "sigstore".
func pluginTrustCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the local trust store for signed plugin packages",
	}
	cmd.AddCommand(
		pluginTrustAddCommand(opts),
		pluginTrustListCommand(opts),
		pluginTrustRemoveCommand(opts),
		pluginTrustAddIdentityCommand(opts),
		pluginTrustRemoveIdentityCommand(opts),
	)
	return cmd
}

func pluginTrustAddCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "add <pubkey-file>",
		Short: "Add a minisign public key to the trust store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ran = true
			id, err := pkgtrust.Add(args[0])
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n", id); err != nil {
				return fmt.Errorf("cmd: trust add: %w", err)
			}
			return nil
		},
	}
}

// pluginTrustListCommand lists both trust stores, scheme-tagged: a
// signing: scheme: "minisign" key from ~/.kevin/trusted-keys, and a
// signing: scheme: "sigstore" identity from ~/.kevin/trusted-identities.
func pluginTrustListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the trust store's keys and identities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			keys, err := pkgtrust.List()
			if err != nil {
				return err
			}
			ids, err := pkgtrust.ListIdentities()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, k := range keys {
				if _, err := fmt.Fprintf(w, "minisign\t%s\t%s\n", k.ID, k.File); err != nil {
					return fmt.Errorf("cmd: trust list: %w", err)
				}
			}
			for _, id := range ids {
				if _, err := fmt.Fprintf(w, "sigstore\t%s\t%s\n", id.Identity, id.Issuer); err != nil {
					return fmt.Errorf("cmd: trust list: %w", err)
				}
			}
			return nil
		},
	}
}

func pluginTrustRemoveCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <key-id>",
		Short: "Remove a key from the trust store",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.ran = true
			return pkgtrust.Remove(args[0])
		},
	}
}

// pluginTrustAddIdentityCommand adds a trusted sigstore identity/issuer
// pair. Unlike pluginTrustAddCommand's key file, an identity and issuer are
// arbitrary strings named on the command line, not read from a file -
// separate verbs since a key-file path and an identity/issuer pair aren't
// reliably distinguishable from one positional argument.
func pluginTrustAddIdentityCommand(opts *options) *cobra.Command {
	var identity, issuer string
	c := &cobra.Command{
		Use:   "add-identity",
		Short: "Trust a sigstore signing identity",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			opts.ran = true
			return pkgtrust.AddIdentity(identity, issuer)
		},
	}
	c.Flags().StringVar(&identity, "identity", "", "the trusted certificate identity (e.g. an email, or a GitHub Actions workflow-ref URI)")
	c.Flags().StringVar(&issuer, "issuer", "", "the trusted OIDC issuer URL (e.g. https://token.actions.githubusercontent.com)")
	_ = c.MarkFlagRequired("identity") // static flag name, cannot fail
	_ = c.MarkFlagRequired("issuer")   // static flag name, cannot fail
	return c
}

func pluginTrustRemoveIdentityCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "remove-identity <identity> <issuer>",
		Short: "Remove a sigstore signing identity from the trust store",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			opts.ran = true
			return pkgtrust.RemoveIdentity(args[0], args[1])
		},
	}
}
