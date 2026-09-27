package pluginindex

import (
	"bytes"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
)

// Snippet renders v's source field as a pasteable "plugins: <name>: {...}"
// CUE block, ready to paste into kevin.cue. It renders v's own validated
// value, not a re-marshaled Go struct, so the output can never drift from
// the schema.
func Snippet(v Version, name string) ([]byte, error) {
	source := v.value.LookupPath(cue.ParsePath("source"))
	if err := source.Err(); err != nil {
		return nil, fmt.Errorf("pluginindex: render snippet: %w", err)
	}

	root := cuecontext.New().CompileString("plugins: {}")
	root = root.FillPath(cue.MakePath(cue.Str("plugins"), cue.Str(name)), source)
	if err := root.Err(); err != nil {
		return nil, fmt.Errorf("pluginindex: render snippet: %w", err)
	}

	// cue.Docs(false) is a no-op in the pinned cue version; strip comments
	// from the syntax tree instead.
	node := root.Syntax(cue.Final())
	ast.Walk(node, func(n ast.Node) bool {
		ast.SetComments(n, nil)
		return true
	}, nil)

	// Lift the struct's fields into an *ast.File to format as bare
	// top-level declarations, not a redundant outer "{...}" literal.
	structLit, ok := node.(*ast.StructLit)
	if !ok {
		return nil, fmt.Errorf("pluginindex: render snippet: unexpected syntax %T", node)
	}
	b, err := format.Node(&ast.File{Decls: structLit.Elts})
	if err != nil {
		return nil, fmt.Errorf("pluginindex: render snippet: %w", err)
	}
	return bytes.TrimSpace(b), nil
}
