package config

import (
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// ValidateAgainstSchema checks rendered - a fully rendered with or config
// block, with no "${...}" markers left in it - against schemaSrc, that
// block's own published #Config schema. label names the block in an error
// message (e.g. "a.with", "plugins.echo.config").
//
// A block with an empty schemaSrc, or one whose schema declares no #Config,
// is skipped: that shape would already have failed earlier in the pipeline
// (see [File.validateStep] and [File.validatePluginConfigs]) - this exists
// to catch a mismatch [File.validateStep]/[File.validatePluginConfigs]
// couldn't yet: a bare "${...}" marker's real value, known only once a step
// renders its with block at Up time, or a plugin's config block resolves
// against vars/env/project, that doesn't actually satisfy the field
// consuming it - e.g. a variable declared "type: string" filling a with
// field the plugin's own schema types "int".
func ValidateAgainstSchema(label string, rendered, schemaSrc []byte) error {
	if len(schemaSrc) == 0 {
		return nil
	}
	ctx := cuecontext.New()
	schemaVal := ctx.CompileBytes(schemaSrc)
	if err := schemaVal.Err(); err != nil {
		return fmt.Errorf("config: %s schema: %w", label, err)
	}
	cfgSchema := schemaVal.LookupPath(cue.ParsePath("#Config"))
	if !cfgSchema.Exists() {
		return nil
	}
	val := ctx.CompileBytes(rendered)
	if err := val.Err(); err != nil {
		return fmt.Errorf("config: %s: %w", label, err)
	}
	merged := cfgSchema.Unify(val)
	if err := merged.Validate(cue.Concrete(true)); err != nil {
		return fmt.Errorf("config: %s: %w: %w", label, ErrInvalid, err)
	}
	return nil
}
