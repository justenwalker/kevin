package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spf13/cobra"

	"github.com/justenwalker/kevin/internal/cueschema"
)

const (
	referenceDefaultGlob   = "internal/plugins/*/schema.cue"
	referenceDefaultOutDir = "docs/site/content/docs/reference/steps"
	referenceTemplateName  = "reference.md.tmpl"
	outputDirPerm          = 0o755 // caller-controlled via --out, not attacker input
)

// referenceCommand builds the reference command: one or more schema.cue
// globs (default internal/plugins/*/schema.cue) rendered into --out.
func referenceCommand() *cobra.Command {
	var outputDir string

	cmd := &cobra.Command{
		Use:   "reference [glob ...]",
		Short: "Render reference doc pages from a plugin's schema.cue and reference.md.tmpl",
		Args:  cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, globs []string) error {
			if len(globs) == 0 {
				globs = []string{referenceDefaultGlob}
			}
			return renderAllReference(globs, outputDir)
		},
	}
	cmd.Flags().StringVar(&outputDir, "out", referenceDefaultOutDir, "directory to write rendered .md pages into")
	return cmd
}

// renderAllReference expands every glob in globs and renders each matched
// schema.cue into outputDir.
func renderAllReference(globs []string, outputDir string) error {
	var schemas []string
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			return fmt.Errorf("gen-docs: reference: glob %s: %w", g, err)
		}
		schemas = append(schemas, matches...)
	}
	for _, schemaPath := range schemas {
		if err := renderReference(schemaPath, outputDir); err != nil {
			return err
		}
	}
	return nil
}

func renderReference(schemaPath, outputDir string) error {
	dir := filepath.Dir(schemaPath)
	tmplPath := filepath.Join(dir, referenceTemplateName)
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
	tmpl := template.New(fieldTableName).Delims("[[", "]]").Funcs(referenceFuncs)
	if tmpl, err = tmpl.Parse(fieldTable); err != nil {
		return fmt.Errorf("gen-docs: reference: parse the built-in %s template: %w", fieldTableName, err)
	}
	if tmpl, err = tmpl.New("banner").Parse(referenceBanner); err != nil {
		return fmt.Errorf("gen-docs: reference: parse the built-in banner template: %w", err)
	}
	if tmpl, err = tmpl.ParseFiles(tmplPath); err != nil {
		return fmt.Errorf("gen-docs: reference: parse %s: %w", tmplPath, err)
	}

	if err = os.MkdirAll(outputDir, outputDirPerm); err != nil {
		return fmt.Errorf("gen-docs: reference: create %s: %w", outputDir, err)
	}

	outPath := filepath.Join(outputDir, filepath.Base(dir)+".md")
	out, err := os.Create(outPath) //nolint:gosec // outputDir is caller-controlled (flag), same trust level as any other CLI output path
	if err != nil {
		return fmt.Errorf("gen-docs: reference: create %s: %w", outPath, err)
	}
	defer out.Close() //nolint:errcheck // the file is opened read-write only to be written and closed

	if err = tmpl.ExecuteTemplate(out, filepath.Base(tmplPath), schema); err != nil {
		return fmt.Errorf("gen-docs: reference: render %s: %w", tmplPath, err)
	}
	return nil
}

// referenceBanner is the shared DO-NOT-EDIT notice every reference.md.tmpl
// calls via [[template "banner"]], placed right after the page's front
// matter (front matter must stay the first thing in the file, so the
// banner can't just be prepended for the caller).
//
//go:embed banner_reference.md.tmpl
var referenceBanner string

const fieldTableName = "table"

// fieldTable is the shared table renderer every reference.md.tmpl calls via
// [[template "table" someDefinition]]. Leaves no trailing newline after the
// last row, so the calling template's own surrounding whitespace fully
// controls the spacing around it.
//
//go:embed field_table.md.tmpl
var fieldTable string

var referenceFuncs = template.FuncMap{
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
