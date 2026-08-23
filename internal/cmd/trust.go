package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/pkgtrust"
)

// pluginTrustCommand groups the subcommands that manage the local minisign
// trust store (~/.kevin/trusted-keys) a plugins.<name> entry's signed: true
// verifies against.
func pluginTrustCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Manage the local trust store for signed plugin packages",
	}
	cmd.AddCommand(pluginTrustAddCommand(opts), pluginTrustListCommand(opts), pluginTrustRemoveCommand(opts))
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

func pluginTrustListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the keys in the trust store",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			keys, err := pkgtrust.List()
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			for _, k := range keys {
				if _, err := fmt.Fprintf(w, "%s\t%s\n", k.ID, k.File); err != nil {
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
