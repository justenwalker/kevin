package config

import (
	"bytes"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// ApplySchema checks rendered - a fully rendered with or config block, with
// no "${...}" markers left in it - against schemaSrc, that block's own
// published #Config schema, and returns the block as JSON with every default
// the schema declares filled in. An empty rendered block is an empty object.
//
// A block with an empty schemaSrc, or one whose schema declares no #Config,
// is returned unchanged: that shape would already have failed earlier in the
// pipeline (see [File.validateStep] and [File.validatePluginConfigs]) - the
// check exists to catch a mismatch they couldn't yet: a bare "${...}"
// marker's real value, known only once a step renders its with block at Up
// time, or a plugin's config block resolves against vars/env/project, that
// doesn't actually satisfy the field consuming it - e.g. a variable declared
// "type: string" filling a with field the plugin's own schema types "int".
//
// A default only appears for a field the schema declares without "?", so a
// plugin sees it whether or not the user wrote the field.
func ApplySchema(label string, rendered, schemaSrc []byte) ([]byte, error) {
	if len(schemaSrc) == 0 {
		return rendered, nil
	}
	ctx := cuecontext.New()
	schemaVal := ctx.CompileBytes(schemaSrc)
	if err := schemaVal.Err(); err != nil {
		return nil, fmt.Errorf("config: %s schema: %w", label, err)
	}
	cfgSchema := schemaVal.LookupPath(cue.ParsePath("#Config"))
	if !cfgSchema.Exists() {
		return rendered, nil
	}
	if len(bytes.TrimSpace(rendered)) == 0 {
		rendered = []byte("{}")
	}
	val := ctx.CompileBytes(rendered)
	if err := val.Err(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", label, err)
	}
	merged := cfgSchema.Unify(val)
	if err := merged.Validate(cue.Concrete(true)); err != nil {
		return nil, fmt.Errorf("config: %s: %w: %w", label, ErrInvalid, err)
	}
	out, err := merged.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("config: %s: encode: %w", label, err)
	}
	return out, nil
}
