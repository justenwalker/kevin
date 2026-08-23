// Command gen-reference-docs renders reference doc pages from a plugin's
// schema.cue and reference.md.tmpl.
//
// For every schema.cue matched by its glob args (default
// internal/plugins/*/schema.cue) with a sibling reference.md.tmpl, the
// schema is reduced to a cueschema.Schema and the template is executed
// against it. reference.md.tmpl owns everything that isn't mechanical -
// front matter, the intro, the worked example, trailing notes - using the
// built-in "table" template (see fieldTable) wherever a field table
// belongs. --out sets the output directory (default
// docs/site/content/docs/reference); a third-party plugin repo passes its
// own schema.cue path(s) and --out to render into its own docs tree:
//
//	gen-reference-docs --out ./docs ./schema.cue
package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/cueschema"
)

const (
	defaultGlob      = "internal/plugins/*/schema.cue"
	defaultOutputDir = "docs/site/content/docs/reference"
	templateName     = "reference.md.tmpl"
	outputDirPerm    = 0o755 // caller-controlled via --out, not attacker input
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
		errPrintln("gen-reference-docs:", err)
		return 1
	}
	return 0
}

// rootCommand builds the gen-reference-docs command: one or more schema.cue
// globs (default internal/plugins/*/schema.cue) rendered into --out.
func rootCommand() *cobra.Command {
	var outputDir string

	cmd := &cobra.Command{
		Use:           "gen-reference-docs [glob ...]",
		Short:         "Render reference doc pages from a plugin's schema.cue and reference.md.tmpl",
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, globs []string) error {
			if len(globs) == 0 {
				globs = []string{defaultGlob}
			}
			return renderAll(globs, outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "out", defaultOutputDir, "directory to write rendered .md pages into")
	return cmd
}

// renderAll expands every glob in globs and renders each matched schema.cue
// into outputDir.
func renderAll(globs []string, outputDir string) error {
	var schemas []string
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			return fmt.Errorf("gen-reference-docs: glob %s: %w", g, err)
		}
		schemas = append(schemas, matches...)
	}
	for _, schemaPath := range schemas {
		if err := render(schemaPath, outputDir); err != nil {
			return err
		}
	}
	return nil
}

func render(schemaPath, outputDir string) error {
	dir := filepath.Dir(schemaPath)
	tmplPath := filepath.Join(dir, templateName)
	if _, err := os.Stat(tmplPath); err != nil {
		return nil // no reference.md.tmpl: this plugin has no reference page.
	}

	schema, err := cueschema.Parse(schemaPath)
	if err != nil {
		return err
	}

	// [[ ]] instead of the default {{ }}: a reference page's prose uses
	// Hugo shortcodes like {{< relref ... >}}, which would otherwise
	// collide with this tool's own template syntax.
	tmpl := template.New(fieldTableName).Delims("[[", "]]").Funcs(funcs)
	if tmpl, err = tmpl.Parse(fieldTable); err != nil {
		return fmt.Errorf("gen-reference-docs: parse the built-in %s template: %w", fieldTableName, err)
	}
	if tmpl, err = tmpl.New(bannerName).Parse(banner); err != nil {
		return fmt.Errorf("gen-reference-docs: parse the built-in %s template: %w", bannerName, err)
	}
	if tmpl, err = tmpl.ParseFiles(tmplPath); err != nil {
		return fmt.Errorf("gen-reference-docs: parse %s: %w", tmplPath, err)
	}

	if err = os.MkdirAll(outputDir, outputDirPerm); err != nil {
		return fmt.Errorf("gen-reference-docs: create %s: %w", outputDir, err)
	}

	outPath := filepath.Join(outputDir, filepath.Base(dir)+".md")
	out, err := os.Create(outPath) //nolint:gosec // outputDir is caller-controlled (flag), same trust level as any other CLI output path/
	if err != nil {
		return fmt.Errorf("gen-reference-docs: create %s: %w", outPath, err)
	}
	defer out.Close() //nolint:errcheck // the file is opened read-write only to be written and closed

	if err = tmpl.ExecuteTemplate(out, filepath.Base(tmplPath), schema); err != nil {
		return fmt.Errorf("gen-reference-docs: render %s: %w", tmplPath, err)
	}
	return nil
}

const bannerName = "banner"

// banner is the shared DO-NOT-EDIT notice every reference.md.tmpl calls
// via [[template "banner"]], placed right after the page's front matter
// (front matter must stay the first thing in the file, so the banner
// can't just be prepended for the caller).
//
//go:embed banner.md.tmpl
var banner string

const fieldTableName = "table"

// fieldTable is the shared table renderer every reference.md.tmpl calls
// via [[template "table" someDefinition]]. Leaves no trailing newline
// after the last row, so the calling template's own surrounding
// whitespace fully controls the spacing around it.
//
//go:embed field_table.md.tmpl
var fieldTable string

var funcs = template.FuncMap{
	"backtick": func(s string) string {
		if s == "" {
			return "-"
		}
		return "`" + s + "`"
	},
	"enum": func(alts []string) string {
		quoted := make([]string, len(alts))
		for i, a := range alts {
			quoted[i] = "`" + a + "`"
		}
		return strings.Join(quoted, ` \| `)
	},
}

func errPrintln(v ...any) {
	_, _ = fmt.Fprintln(os.Stderr, v...)
}
