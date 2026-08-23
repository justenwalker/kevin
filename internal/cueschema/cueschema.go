// Package cueschema reads a plugin's schema.cue and reduces it to a plain
// Go structure that a text/template can range over, so a doc generator
// depends on this package's types instead of the shape of the CUE AST.
package cueschema

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// Schema is every top-level #Definition in one schema.cue, keyed by name
// with the leading "#" stripped (e.g. "Config", "Expose"), so a template
// can look one up by name: {{.Definitions.Config}}.
type Schema struct {
	Definitions map[string]Definition
}

// Definition is one top-level #Name: {...} struct, such as #Config or
// #Expose.
type Definition struct {
	Name   string // "#Config", "#Expose", ...
	Fields []Field
}

// Field is one field of a Definition.
type Field struct {
	Name     string
	Required bool

	// Type is the field's type, rendered for display (e.g. "string",
	// "bool", "[...string]", "[...#Expose]"). Empty when Enum is set.
	Type string

	// Enum holds each alternative's literal text (already quoted, e.g.
	// `"tcp"`) when the field's type is a disjunction of quoted string
	// literals. Type is empty in this case; a template renders Enum
	// instead.
	Enum []string

	// Default is the field's default literal, rendered for display, or
	// empty when the field has no explicit `| *value` default.
	Default string

	// Doc is the field's doc comment, joined into one string, with the
	// leading "<fieldname> is/are/..." restated dropped and the first
	// remaining word capitalized, so a template can use it as a sentence
	// about the field rather than a restatement of the field's name.
	Doc string
}

// Parse reads and reduces one schema.cue file.
func Parse(path string) (*Schema, error) {
	src, err := os.ReadFile(path) //nolint:gosec // path is a fixed, known schema.cue location, not user input
	if err != nil {
		return nil, fmt.Errorf("cueschema: read %s: %w", path, err)
	}
	f, err := parser.ParseFile(path, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("cueschema: parse %s: %w", path, err)
	}

	s := Schema{Definitions: map[string]Definition{}}
	for _, decl := range f.Decls {
		field, ok := decl.(*ast.Field)
		if !ok {
			continue
		}
		name := labelName(field.Label)
		if !strings.HasPrefix(name, "#") {
			continue
		}
		structLit, ok := field.Value.(*ast.StructLit)
		if !ok {
			continue
		}
		s.Definitions[strings.TrimPrefix(name, "#")] = Definition{
			Name:   name,
			Fields: fields(structLit),
		}
	}
	return &s, nil
}

func fields(s *ast.StructLit) []Field {
	var out []Field
	for _, elt := range s.Elts {
		astField, ok := elt.(*ast.Field)
		if !ok {
			continue
		}
		typ, enum, def := typeEnumDefault(astField.Value)
		out = append(out, Field{
			Name:     labelName(astField.Label),
			Required: astField.Constraint == token.NOT,
			Type:     typ,
			Enum:     enum,
			Default:  def,
			Doc:      doc(astField),
		})
	}
	return out
}

func labelName(l ast.Label) string {
	ident, ok := l.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

func doc(field *ast.Field) string {
	var lines []string
	for _, cg := range ast.Comments(field) {
		if !cg.Doc {
			continue
		}
		// cg.Text() joins a multi-line comment's own lines with "\n",
		// which would otherwise land as a literal newline inside a
		// markdown table cell and break the row.
		lines = append(lines, strings.Join(strings.Fields(cg.Text()), " "))
	}
	text := strings.Join(lines, " ")
	// The doc comment's first word restates the field name, often
	// followed by a copula ("image is the container image to run.");
	// drop both so Doc reads as a sentence about the field, not a
	// restatement of the field's name.
	if _, rest, ok := strings.Cut(text, " "); ok {
		text = rest
	}
	for _, copula := range []string{"is ", "are "} {
		if after, ok := strings.CutPrefix(text, copula); ok {
			text = after
			break
		}
	}
	// quoted and placeholder are tried at each position in that order, so
	// a placeholder inside an already-quoted span (e.g. "socks5://<relay>/...")
	// is consumed as part of the quoted match and never reconsidered on
	// its own - matching it again would nest a second backtick pair
	// inside the first and split one code span into three.
	text = codeSpan.ReplaceAllString(text, "`$0`")
	if text == "" {
		return text
	}
	return strings.ToUpper(text[:1]) + text[1:]
}

// codeSpan matches either a whole "..." quoted string or a bare
// placeholder like <step>.<domain>, backtick-wrapped as one span rather
// than two so the dot between them isn't left stranded outside any code
// span. A placeholder like <step>.<domain> would otherwise reach
// goldmark's unsafe HTML passthrough as unrecognized tags and vanish
// from the rendered page; a code span renders it as literal text instead.
var codeSpan = regexp.MustCompile(`"[^"]*"|<[a-zA-Z]+>(?:\.<[a-zA-Z]+>)*`)

// typeEnumDefault renders a field's type expression, splitting out an
// explicit `| *value` default when present. An enum of quoted string
// alternatives (e.g. "tcp" | "udp" | *"tcp") is returned via enum, one
// entry per alternative; any other disjunction with a default returns
// just the non-default arm as typ.
//
//nolint:nonamedreturns // three same-ish-typed returns; the names document which is which
func typeEnumDefault(expr ast.Expr) (typ string, enum []string, def string) {
	if s, ok := expr.(*ast.StructLit); ok {
		if inline := singleFieldStruct(s); inline != "" {
			return inline, nil, ""
		}
	}

	src := formatNode(expr)

	parts := strings.Split(src, " | ")
	if len(parts) == 1 {
		return src, nil, ""
	}

	// CUE convention lists every alternative, then repeats the default
	// one marked with a leading "*" (e.g. `"tcp" | "udp" | *"tcp"`) - so
	// the default's own text is usually already present as a plain
	// alternative too; only add it again if it genuinely isn't.
	var alts []string
	seen := map[string]bool{}
	for _, part := range parts {
		v, isDefault := strings.CutPrefix(part, "*")
		if isDefault {
			def = v
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		alts = append(alts, v)
	}

	allQuoted := true
	for _, a := range alts {
		if !strings.HasPrefix(a, `"`) {
			allQuoted = false
			break
		}
	}
	if allQuoted {
		return "", alts, def
	}

	// Scalar-with-default: the one non-default alternative is the type.
	for _, a := range alts {
		if a != def {
			typ = a
		}
	}
	return typ, nil, def
}

// singleFieldStruct renders a pattern-constraint field's implicit
// single-field struct value - e.g. `env?: [string]: string`, whose value
// is itself the struct `[string]: string` - as that shorthand directly.
// format.Node on the *ast.StructLit would otherwise expand it to its
// brace-delimited canonical form. Returns "" for any other struct shape.
func singleFieldStruct(s *ast.StructLit) string {
	if len(s.Elts) != 1 {
		return ""
	}
	inner, ok := s.Elts[0].(*ast.Field)
	if !ok {
		return ""
	}
	pattern, ok := inner.Label.(*ast.ListLit)
	if !ok || len(pattern.Elts) != 1 {
		return ""
	}
	return "[" + formatNode(pattern.Elts[0]) + "]: " + formatNode(inner.Value)
}

func formatNode(n ast.Node) string {
	b, err := format.Node(n)
	if err != nil {
		panic(err)
	}
	return string(bytes.TrimSpace(b))
}
