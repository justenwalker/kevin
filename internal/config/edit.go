package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"

	"github.com/justenwalker/kevin/internal/uerr"
)

// InsertPlugin merges snippet - CUE source shaped like
// "plugins: <name>: {...}" (e.g. from pluginindex.Snippet) - into dir's
// environment file (see [Load]) and writes it back, preserving the rest
// of the file including comments.
//
// Returns [ErrUnsupportedEdit] for a YAML/JSON or package-mode CUE
// environment (plugins: could live in any of several files in package
// mode - InsertPlugin does not guess which), naming the file to edit by
// hand instead. Returns [ErrPluginAlreadyDeclared] if the file already
// has a plugins.<name> entry for snippet's own plugin name -
// InsertPlugin never overwrites an existing entry.
func InsertPlugin(dir, name string, snippet []byte) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("config: abs path %q: %w", dir, err)
	}
	path, err := findFile(abs, name)
	if err != nil {
		return err
	}

	if filepath.Ext(path) != cueExt {
		return uerr.Wrap(fmt.Errorf("config: %q: %w", path, ErrUnsupportedEdit),
			"%s is not a CUE file - paste this snippet into it by hand:\n\n%s", path, snippet)
	}
	pkg, err := cuePackageName(path)
	if err != nil {
		return err
	}
	if pkg != "" {
		return uerr.Wrap(fmt.Errorf("config: %q: %w", path, ErrUnsupportedEdit),
			"%s declares a package, so plugins: could live in any file sharing it - paste this snippet in by hand:\n\n%s", path, snippet)
	}

	src, err := os.ReadFile(path) //nolint:gosec // path comes from findFile, resolved against the project directory
	if err != nil {
		return fmt.Errorf("config: read %q: %w", path, err)
	}
	targetFile, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("config: parse %q: %w", path, err)
	}
	snippetName, snippetField, err := parseSnippet(snippet)
	if err != nil {
		return err
	}

	if mergeErr := mergePluginField(targetFile, snippetName, snippetField); mergeErr != nil {
		return fmt.Errorf("config: %q: %w", path, mergeErr)
	}

	formatted, err := format.Node(targetFile)
	if err != nil {
		return fmt.Errorf("config: format %q: %w", path, err)
	}

	// Fail fast (before writing anything): the merge must still produce a
	// file that unifies with the core schema, the same check Load itself
	// performs.
	ctx := cuecontext.New()
	schema := mustCompileCoreSchema(ctx)
	user := ctx.CompileBytes(formatted, cue.Filename(path))
	if value := schema.Unify(user); value.Err() != nil {
		return newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, value.Err()))
	}

	return writeFileAtomic(path, formatted)
}

// parseSnippet parses snippet (shaped like "plugins: <name>: {...}") and
// returns the plugin's name and its "<name>: {...}" field.
func parseSnippet(snippet []byte) (string, *ast.Field, error) {
	f, err := parser.ParseFile("snippet", snippet)
	if err != nil {
		return "", nil, fmt.Errorf("config: parse plugin snippet: %w", err)
	}
	if len(f.Decls) != 1 {
		return "", nil, fmt.Errorf("config: plugin snippet: expected one top-level field, got %d", len(f.Decls))
	}
	pluginsField, ok := f.Decls[0].(*ast.Field)
	if !ok {
		return "", nil, fmt.Errorf("config: plugin snippet: expected a field, got %T", f.Decls[0])
	}
	pluginsStruct, ok := pluginsField.Value.(*ast.StructLit)
	if !ok || len(pluginsStruct.Elts) != 1 {
		return "", nil, errors.New("config: plugin snippet: expected plugins: { <name>: {...} }")
	}
	nameField, ok := pluginsStruct.Elts[0].(*ast.Field)
	if !ok {
		return "", nil, fmt.Errorf("config: plugin snippet: expected a field, got %T", pluginsStruct.Elts[0])
	}
	name, _, err := ast.LabelName(nameField.Label)
	if err != nil {
		return "", nil, fmt.Errorf("config: plugin snippet: %w", err)
	}
	return name, nameField, nil
}

// mergePluginField inserts nameField into targetFile's top-level plugins
// struct, creating it if absent.
//
// Returns [ErrPluginAlreadyDeclared] if a field named name already exists
// under plugins.
func mergePluginField(targetFile *ast.File, name string, nameField *ast.Field) error {
	for _, decl := range targetFile.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		label, _, err := ast.LabelName(field.Label)
		if err != nil || label != "plugins" {
			continue
		}
		pluginsStruct, ok := field.Value.(*ast.StructLit)
		if !ok {
			return fmt.Errorf("plugins: is not a struct literal, edit it by hand: %w", ErrUnsupportedEdit)
		}
		for _, existing := range pluginsStruct.Elts {
			existingField, ok := existing.(*ast.Field)
			if !ok {
				continue
			}
			existingName, _, err := ast.LabelName(existingField.Label)
			if err == nil && existingName == name {
				return fmt.Errorf("plugins.%s: %w", name, ErrPluginAlreadyDeclared)
			}
		}
		pluginsStruct.Elts = append(pluginsStruct.Elts, nameField)
		return nil
	}

	// No existing top-level plugins field - the snippet's own field
	// (which parseSnippet already validated is "plugins: { <name>: {...}
	// }") can be appended wholesale.
	targetFile.Decls = append(targetFile.Decls, &ast.Field{
		Label: ast.NewIdent("plugins"),
		Value: &ast.StructLit{Elts: []ast.Decl{nameField}},
	})
	return nil
}

// writeFileAtomic writes data to path by creating a temp file in the same
// directory and renaming it into place, so a crash or interrupt mid-write
// never leaves path half-written.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".kevin-edit-*")
	if err != nil {
		return fmt.Errorf("config: create temp file: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup; the rename below removes it on success

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write %q: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close %q: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: install %q: %w", path, err)
	}
	return nil
}
