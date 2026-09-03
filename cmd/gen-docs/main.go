// Command gen-docs renders kevin's reference doc pages: "reference" for a
// plugin's schema.cue, "commands" for kevin's own cobra command tree.
//
// reference renders every schema.cue matched by its glob args (default
// internal/plugins/*/schema.cue) with a sibling reference.md.tmpl: the
// schema is reduced to a cueschema.Schema and the template executed
// against it, using the built-in "table" template wherever a field table
// belongs. --out sets the output directory (default
// docs/site/content/docs/reference/steps); a third-party plugin repo
// passes its own schema.cue path(s) and --out to render into its own docs
// tree:
//
//	gen-docs reference --out ./docs ./schema.cue
//
// commands renders every top-level kevin command with a sibling
// reference.md.tmpl in internal/cmd/reference/: the command (and its
// subcommands) is reduced to a cmdschema.Command and the template executed
// against it, using the built-in "flags" template wherever a flag table
// belongs. --out sets the output directory (default
// docs/site/content/docs/reference/commands). A command or plugin with no
// reference.md.tmpl gets no page.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), interruptSignals...)
	rc := run(ctx, os.Args)
	cancel()
	os.Exit(rc)
}

func run(ctx context.Context, args []string) int {
	root := rootCommand()
	root.SetArgs(args[1:])
	if err := root.ExecuteContext(ctx); err != nil {
		errPrintln("gen-docs:", err)
		return 1
	}
	return 0
}

// rootCommand builds the gen-docs command: reference and commands each
// render one family of doc pages.
func rootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "gen-docs",
		Short:         "Render kevin's reference doc pages",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.AddCommand(referenceCommand(), commandsCommand())
	return cmd
}

func errPrintln(v ...any) {
	_, _ = fmt.Fprintln(os.Stderr, v...)
}
