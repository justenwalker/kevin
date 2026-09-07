package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/docker"
	trustinstall "github.com/justenwalker/kevin/internal/trust"
	"github.com/justenwalker/kevin/internal/uerr"
)

// doctorCommand checks Docker, the kevin CA, and (if a project directory
// holds an environment file) its console/proxy ports. It changes nothing:
// no store is installed into, no port is left bound.
func doctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and project for common setup problems",
		Long: "doctor checks whether Docker is reachable, whether the kevin root CA is trusted, " +
			"and whether the project's console/proxy ports are free. It creates nothing and " +
			"changes no trust store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			w := cmd.OutOrStdout()
			ok := true

			if err := (docker.Client{}).Available(cmd.Context()); err != nil {
				printCheck(w, "docker", false, false, uerr.Display(err))
				ok = false
			} else {
				printCheck(w, "docker", true, false, "")
			}

			if !checkCA(cmd, w) {
				ok = false
			}

			if !checkPorts(cmd.Context(), w, opts) {
				ok = false
			}

			if !ok {
				return errors.New("cmd: doctor: one or more checks failed")
			}
			return nil
		},
	}
}

// checkCA reports one line per trust store this machine has, without
// installing into any of them.
func checkCA(cmd *cobra.Command, w io.Writer) bool {
	ok := true
	results, err := trustinstall.Status(cmd.Context(), requestFor(false, true))
	for _, r := range results {
		switch {
		case r.Skipped:
			printCheck(w, "CA "+r.Store, false, true, r.Reason)
		case r.Installed:
			printCheck(w, "CA "+r.Store, true, false, r.Reason)
		default:
			printCheck(w, "CA "+r.Store, false, false, `not trusted - run "kevin ca install"`)
			ok = false
		}
	}
	if err != nil {
		printCheck(w, "CA", false, false, err.Error())
		ok = false
	}
	return ok
}

// checkPorts reports whether the project's console and proxy listen
// addresses are free to bind. A directory with no environment file is a
// skip, not a failure.
func checkPorts(ctx context.Context, w io.Writer, opts *options) bool {
	f, err := config.Load(opts.dir, opts.name, opts.tags)
	if errors.Is(err, config.ErrNotFound) {
		printCheck(w, "ports", false, true, "no environment file in this directory")
		return true
	}
	if err != nil {
		printCheck(w, "ports", false, false, uerr.Display(err))
		return false
	}
	cfg, err := f.Config()
	if err != nil {
		printCheck(w, "ports", false, false, uerr.Display(err))
		return false
	}

	ok := true
	var lc net.ListenConfig
	for _, p := range []struct{ name, addr string }{
		{"console", cfg.Console.Listen},
		{"proxy", cfg.Proxy.Listen},
	} {
		ln, err := lc.Listen(ctx, "tcp", p.addr)
		if err != nil {
			printCheck(w, "port "+p.name+" ("+p.addr+")", false, false, "in use")
			ok = false
			continue
		}
		_ = ln.Close()
		printCheck(w, "port "+p.name+" ("+p.addr+")", true, false, "")
	}
	printCheck(w, "port gateway_port", false, true,
		"binds on the docker network gateway once it exists, not checkable before run")
	return ok
}

// printCheck writes one line reporting a check's outcome: "ok", "ok
// (detail)", "fail (detail)", or "skip (detail)".
func printCheck(w io.Writer, name string, ok, skip bool, detail string) {
	switch {
	case skip:
		_, _ = fmt.Fprintf(w, "%s: skip (%s)\n", name, detail)
	case ok:
		if detail == "" {
			_, _ = fmt.Fprintf(w, "%s: ok\n", name)
			return
		}
		_, _ = fmt.Fprintf(w, "%s: ok (%s)\n", name, detail)
	default:
		_, _ = fmt.Fprintf(w, "%s: fail (%s)\n", name, detail)
	}
}
