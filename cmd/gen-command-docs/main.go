// Command gen-command-docs renders reference doc pages from kevin's cobra
// command tree.
//
// For every top-level command with a sibling reference.md.tmpl in
// internal/cmd/reference/, the command (and its subcommands) is reduced to
// a cmdschema.Command and the template is executed against it.
// reference.md.tmpl owns everything that isn't mechanical - front matter,
// the intro, worked examples, trailing notes - using the built-in "flags"
// template wherever a flag table belongs. --out sets the output directory
// (default docs/site/content/docs/commands). A command with no
// reference.md.tmpl gets no page.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"

	kevincmd "github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/cmdschema"
)

const (
	templateDir   = "internal/cmd/reference"
	defaultOutDir = "docs/site/content/docs/commands"
	outputDirPerm = 0o755 // caller-controlled via --out, not attacker input
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
		errPrintln("gen-command-docs:", err)
		return 1
	}
	return 0
}

// rootCommand builds the gen-command-docs command: no positional args,
// every top-level kevin command with a reference.md.tmpl is rendered into
// --out.
func rootCommand() *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:           "gen-command-docs",
		Short:         "Render reference doc pages from kevin's cobra command tree",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return renderAll(outDir)
		},
	}
	cmd.Flags().StringVar(&outDir, "out", defaultOutDir, "directory to write rendered .md pages into")
	return cmd
}

func renderAll(outDir string) error {
	root, _ := kevincmd.NewRootCommand()
	for _, c := range root.Commands() {
		if err := render(c, outDir); err != nil {
			return err
		}
	}
	return nil
}

func render(c *cobra.Command, outDir string) error {
	tmplPath := filepath.Join(templateDir, c.Name()+".md.tmpl")
	if _, err := os.Stat(tmplPath); err != nil {
		return nil // no reference.md.tmpl: this command has no reference page.
	}

	// [[ ]] instead of the default {{ }}: a reference page's prose uses
	// Hugo shortcodes like {{< relref ... >}}, which would otherwise
	// collide with this tool's own template syntax.
	tmpl := template.New(flagsName).Delims("[[", "]]").Funcs(funcs)
	tmpl, err := tmpl.Parse(flagsTable)
	if err != nil {
		return fmt.Errorf("gen-command-docs: parse the built-in %s template: %w", flagsName, err)
	}
	if tmpl, err = tmpl.New(bannerName).Parse(banner); err != nil {
		return fmt.Errorf("gen-command-docs: parse the built-in %s template: %w", bannerName, err)
	}
	if tmpl, err = tmpl.ParseFiles(tmplPath); err != nil {
		return fmt.Errorf("gen-command-docs: parse %s: %w", tmplPath, err)
	}

	if err = os.MkdirAll(outDir, outputDirPerm); err != nil {
		return fmt.Errorf("gen-command-docs: create %s: %w", outDir, err)
	}

	outPath := filepath.Join(outDir, c.Name()+".md")
	out, err := os.Create(outPath) //nolint:gosec // outDir is caller-controlled (flag), same trust level as any other CLI output path
	if err != nil {
		return fmt.Errorf("gen-command-docs: create %s: %w", outPath, err)
	}
	defer out.Close() //nolint:errcheck // the file is opened read-write only to be written and closed

	if err = tmpl.ExecuteTemplate(out, filepath.Base(tmplPath), cmdschema.FromCommand(c)); err != nil {
		return fmt.Errorf("gen-command-docs: render %s: %w", tmplPath, err)
	}
	return nil
}

const bannerName = "banner"

// banner is the shared DO-NOT-EDIT notice every reference.md.tmpl calls via
// [[template "banner"]], placed right after the page's front matter.
//
//go:embed banner.md.tmpl
var banner string

const flagsName = "flags"

// flagsTable is the shared flag table renderer every reference.md.tmpl
// calls via [[template "flags" someCommand]]. Leaves no trailing newline
// after the last row, so the calling template's own surrounding whitespace
// fully controls the spacing around it. Renders nothing for a command with
// no flags of its own.
//
//go:embed flag_table.md.tmpl
var flagsTable string

var funcs = template.FuncMap{
	"backtick": func(s string) string {
		if s == "" {
			return "-"
		}
		return "`" + s + "`"
	},
	"flagName": func(f cmdschema.Flag) string {
		if f.Shorthand == "" {
			return "`--" + f.Name + "`"
		}
		return "`--" + f.Name + "`, `-" + f.Shorthand + "`"
	},
}

func errPrintln(v ...any) {
	_, _ = fmt.Fprintln(os.Stderr, v...)
}
