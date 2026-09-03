package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"

	kevincmd "github.com/justenwalker/kevin/internal/cmd"
	"github.com/justenwalker/kevin/internal/cmdschema"
)

const (
	commandsTemplateDir   = "internal/cmd/reference"
	commandsDefaultOutDir = "docs/site/content/docs/reference/commands"
)

// commandsCommand builds the commands command: no positional args, every
// top-level kevin command with a reference.md.tmpl is rendered into --out.
func commandsCommand() *cobra.Command {
	var outDir string

	cmd := &cobra.Command{
		Use:   "commands",
		Short: "Render reference doc pages from kevin's cobra command tree",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return renderAllCommands(outDir)
		},
	}
	cmd.Flags().StringVar(&outDir, "out", commandsDefaultOutDir, "directory to write rendered .md pages into")
	return cmd
}

func renderAllCommands(outDir string) error {
	root, _ := kevincmd.NewRootCommand()
	for _, c := range root.Commands() {
		if err := renderCommand(c, outDir); err != nil {
			return err
		}
	}
	return nil
}

func renderCommand(c *cobra.Command, outDir string) error {
	tmplPath := filepath.Join(commandsTemplateDir, c.Name()+".md.tmpl")
	if _, err := os.Stat(tmplPath); err != nil {
		return nil // no reference.md.tmpl: this command has no reference page.
	}

	// [[ ]] instead of the default {{ }}: a reference page's prose uses
	// Hugo shortcodes like {{< relref ... >}}, which would otherwise
	// collide with this tool's own template syntax.
	tmpl := template.New(flagsName).Delims("[[", "]]").Funcs(commandFuncs)
	tmpl, err := tmpl.Parse(flagsTable)
	if err != nil {
		return fmt.Errorf("gen-docs: commands: parse the built-in %s template: %w", flagsName, err)
	}
	if tmpl, err = tmpl.New("banner").Parse(commandBanner); err != nil {
		return fmt.Errorf("gen-docs: commands: parse the built-in banner template: %w", err)
	}
	if tmpl, err = tmpl.ParseFiles(tmplPath); err != nil {
		return fmt.Errorf("gen-docs: commands: parse %s: %w", tmplPath, err)
	}

	if err = os.MkdirAll(outDir, outputDirPerm); err != nil {
		return fmt.Errorf("gen-docs: commands: create %s: %w", outDir, err)
	}

	outPath := filepath.Join(outDir, c.Name()+".md")
	out, err := os.Create(outPath) //nolint:gosec // outDir is caller-controlled (flag), same trust level as any other CLI output path
	if err != nil {
		return fmt.Errorf("gen-docs: commands: create %s: %w", outPath, err)
	}
	defer out.Close() //nolint:errcheck // the file is opened read-write only to be written and closed

	if err = tmpl.ExecuteTemplate(out, filepath.Base(tmplPath), cmdschema.FromCommand(c)); err != nil {
		return fmt.Errorf("gen-docs: commands: render %s: %w", tmplPath, err)
	}
	return nil
}

// commandBanner is the shared DO-NOT-EDIT notice every reference.md.tmpl
// calls via [[template "banner"]], placed right after the page's front
// matter.
//
//go:embed banner_command.md.tmpl
var commandBanner string

const flagsName = "flags"

// flagsTable is the shared flag table renderer every reference.md.tmpl
// calls via [[template "flags" someCommand]]. Leaves no trailing newline
// after the last row, so the calling template's own surrounding whitespace
// fully controls the spacing around it. Renders nothing for a command with
// no flags of its own.
//
//go:embed flag_table.md.tmpl
var flagsTable string

var commandFuncs = template.FuncMap{
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
