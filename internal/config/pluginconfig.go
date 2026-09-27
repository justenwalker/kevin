package config

import (
	"fmt"

	"github.com/justenwalker/kevin/internal/ca"
	"github.com/justenwalker/kevin/internal/expr"
)

// ResolvePluginConfigs renders every declared plugin's config block against
// cfg's own resolved variables and project constants, replacing
// cfg.Plugins[<name>].Config with the rendered result, and re-validates it
// against that plugin's own published config schema.
//
// A config block has no needs/setup scope: no step has run Up yet when
// Configure sends it, so every "${...}" marker a config block carries
// always resolves here, before Configure ever runs - unlike a step's with
// block, which may still carry a needs/setup marker only resolvable at that
// step's own Up call (see [ValidateAgainstSchema]).
func (cfg *Config) ResolvePluginConfigs(schemas map[string]PluginSchemas) error {
	scopes := expr.Scopes{Vars: cfg.VariableValues, Project: ca.ProjectVars(cfg.Dir, cfg.Name)}
	for name, spec := range cfg.Plugins {
		if len(spec.Config) == 0 {
			continue
		}
		rendered, err := expr.Render(spec.Config, name, scopes)
		if err != nil {
			return fmt.Errorf("config: plugins.%s.config: %w", name, err)
		}
		spec.Config = rendered
		cfg.Plugins[name] = spec

		if err := ValidateAgainstSchema("plugins."+name+".config", rendered, schemas[name].Config); err != nil {
			return err
		}
	}
	return nil
}
