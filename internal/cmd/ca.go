package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/ca"
	trustinstall "github.com/justenwalker/kevin/internal/trust"
)

// caCommand groups the subcommands that manage the kevin root CA in the
// machine's trust stores. It needs no project.
func caCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "Manage the kevin root CA in the machine's trust stores",
	}
	cmd.AddCommand(caInstallCommand(opts), caUninstallCommand(opts))
	return cmd
}

func caInstallCommand(opts *options) *cobra.Command {
	var system, firefox bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the kevin root CA into the trust stores of this machine",
		Long: "install generates the kevin root CA if it does not already exist, then adds it " +
			"to the trust stores of this machine. Run this once for the machine; there is no " +
			"need to repeat it per project.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			manager := ca.NewManager("", "", "", ca.Options{})
			if _, err := manager.LoadOrGenerateRoot(); err != nil {
				return fmt.Errorf("cmd: ca install: %w", err)
			}
			results, err := trustinstall.Install(cmd.Context(), requestFor(system, firefox))
			report(cmd.OutOrStdout(), results)
			if err != nil {
				return advise(err, results)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "install into the machine-wide store instead of the store of the user; needs root")
	cmd.Flags().BoolVar(&firefox, "firefox", true, "also install into every Firefox profile's NSS database")
	return cmd
}

func caUninstallCommand(opts *options) *cobra.Command {
	var system, firefox bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the kevin root CA from the trust stores of this machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			results, err := trustinstall.Remove(cmd.Context(), requestFor(system, firefox))
			report(cmd.OutOrStdout(), results)
			if err != nil {
				return advise(err, results)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&system, "system", false, "remove from the machine-wide store instead of the store of the user; needs root")
	cmd.Flags().BoolVar(&firefox, "firefox", true, "also remove from every Firefox profile's NSS database")
	return cmd
}

// requestFor builds the trust request for the kevin root. One entry is
// shared by every project.
func requestFor(system, firefox bool) trustinstall.Request {
	return trustinstall.Request{
		CertPath:   ca.RootCertPath(),
		CommonName: ca.RootCommonName,
		FileName:   "kevin-local-root",
		System:     system,
		Firefox:    firefox,
	}
}

// report writes one line for each store.
func report(w io.Writer, results []trustinstall.Result) {
	for _, r := range results {
		switch {
		case r.Skipped:
			_, _ = fmt.Fprintln(w, r.Store+": skipped, "+r.Reason)
		case r.Installed:
			line := r.Store + ": installed"
			if r.Reason != "" {
				line += " (" + r.Reason + ")"
			}
			_, _ = fmt.Fprintln(w, line)
		default:
			_, _ = fmt.Fprintln(w, r.Store+": removed")
		}
	}
}

// advise adds the command to run by hand when a store needs root.
func advise(err error, results []trustinstall.Result) error {
	if !errors.Is(err, trustinstall.ErrNeedsRoot) {
		return err
	}
	for _, r := range results {
		if r.Reason != "" {
			return fmt.Errorf("%w: %s", err, r.Reason)
		}
	}
	return err
}
