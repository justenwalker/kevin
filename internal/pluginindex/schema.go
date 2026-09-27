package pluginindex

import (
	_ "embed"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	yamlpkg "cuelang.org/go/encoding/yaml"

	"github.com/justenwalker/kevin/internal/config"
)

//go:embed schema.cue
var entrySchema []byte

// schema is the compiled #PluginMeta/#Version definitions, unified with
// kevin's own [config.CoreSchema] in one parse unit so #OCI/#File/#HTTP
// resolve for entrySchema's #Version.source.
var schema cue.Value

//nolint:gochecknoinits
func init() {
	ctx := cuecontext.New()
	src := append(append([]byte{}, config.CoreSchema...), '\n')
	src = append(src, entrySchema...)
	v := ctx.CompileBytes(src, cue.Filename("pluginindex/schema.cue"))
	if err := v.Err(); err != nil {
		panic(fmt.Errorf("pluginindex: compile schema: %w", err))
	}
	schema = v
}

// validateAgainst parses src as YAML and unifies it with the definition at
// defPath (schema's own #PluginMeta or #Version), returning the unified,
// concrete value.
func validateAgainst(defPath cue.Path, path string, src []byte) (cue.Value, error) {
	astFile, err := yamlpkg.Extract(path, src)
	if err != nil {
		return cue.Value{}, fmt.Errorf("pluginindex: parse %q: %w", path, err)
	}
	user := cuecontext.New().BuildFile(astFile)
	if err := user.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("pluginindex: parse %q: %w", path, err)
	}

	v := schema.LookupPath(defPath).Unify(user)
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("pluginindex: %q: %w", path, err)
	}
	if err := v.Validate(cue.Concrete(true)); err != nil {
		return cue.Value{}, fmt.Errorf("pluginindex: %q: %w", path, err)
	}
	return v, nil
}
