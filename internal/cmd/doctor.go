package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/engines"
	trustinstall "github.com/justenwalker/kevin/internal/trust"
	"github.com/justenwalker/kevin/internal/uerr"
)

// doctorCommand checks the container engine, the kevin CA, and (if a project
// directory holds an environment file) its console/proxy ports. It changes
// nothing: no store is installed into, no port is left bound.
func doctorCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and project for common setup problems",
		Long: "doctor checks whether the selected container engine (docker or podman, " +
			"via --engine/KEVIN_ENGINE or auto-detection) is reachable, whether the kevin " +
			"root CA is trusted, and whether the project's console/proxy ports are free. " +
			"It creates nothing and changes no trust store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.ran = true
			w := cmd.OutOrStdout()
			ok := true

			cfg, err := loadProjectConfig(w, opts)
			if err != nil {
				ok = false
			}

			engineName, err := resolveEngineName(cmd.Context(), opts)
			if err != nil {
				printCheck(w, "engine", false, false, uerr.Display(err))
				ok = false
			} else if !checkEngine(cmd.Context(), w, engineName) {
				ok = false
			}

			if !checkCA(cmd, w) {
				ok = false
			}

			if !checkPorts(cmd.Context(), w, cfg) {
				ok = false
			}

			if !ok {
				return errors.New("cmd: doctor: one or more checks failed")
			}
			return nil
		},
	}
}

// loadProjectConfig loads the project's kevin.cue, or reports (nil, nil)
// when this directory holds none - every check below treats an absent
// project as "check the defaults", not a failure.
func loadProjectConfig(w io.Writer, opts *options) (*config.Config, error) {
	f, err := config.Load(opts.dir, opts.name, opts.tags)
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil //nolint:nilnil // no environment file in this directory is a valid, common case
	}
	if err != nil {
		printCheck(w, "config", false, false, uerr.Display(err))
		return nil, err
	}
	cfg, err := f.Config()
	if err != nil {
		printCheck(w, "config", false, false, uerr.Display(err))
		return nil, err
	}
	return cfg, nil
}

// checkEngine probes name, the engine --engine/KEVIN_ENGINE selected or
// auto-detection found - never a project fact, so this needs no cfg.
func checkEngine(ctx context.Context, w io.Writer, name string) bool {
	rt, err := engines.New(name, nil)
	if err != nil {
		printCheck(w, name, false, false, uerr.Display(err))
		return false
	}
	if err := rt.Available(ctx); err != nil {
		printCheck(w, name, false, false, uerr.Display(err))
		return false
	}
	printCheck(w, name, true, false, "")
	return true
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
// addresses are free to bind. A nil cfg (no environment file) is a skip,
// not a failure.
func checkPorts(ctx context.Context, w io.Writer, cfg *config.Config) bool {
	if cfg == nil {
		printCheck(w, "ports", false, true, "no environment file in this directory")
		return true
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
		"binds on the container network gateway once it exists, not checkable before run")
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
