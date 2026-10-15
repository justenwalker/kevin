package config

import (
	"fmt"

	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/statedir"
)

// ResolvePluginConfigs renders every declared plugin's config block against
// cfg's own resolved variables and project constants, replacing
// cfg.Plugins[<name>].Config with the rendered result, and validates it
// against that plugin's own published config schema. The stored block also
// carries every default that schema declares, as a step's with block does.
//
// A config block has no needs/setup scope: no step has run Up yet when
// Configure sends it, so every "${...}" marker a config block carries
// always resolves here, before Configure ever runs - unlike a step's with
// block, which may still carry a needs/setup marker only resolvable at that
// step's own Up call (see [ApplySchema]).
func (cfg *Config) ResolvePluginConfigs(schemas map[string]PluginSchemas) error {
	scopes := expr.Scopes{Env: expr.HostEnv(), Vars: cfg.VariableValues, Project: statedir.For(cfg.Dir, cfg.Name).Vars()}
	for name, spec := range cfg.Plugins {
		if len(spec.Config) == 0 {
			continue
		}
		tpl, err := expr.Parse(spec.Config)
		if err != nil {
			return fmt.Errorf("config: plugins.%s.config: %w", name, err)
		}
		rendered, err := tpl.Render(name, scopes)
		if err != nil {
			return fmt.Errorf("config: plugins.%s.config: %w", name, err)
		}
		merged, err := ApplySchema("plugins."+name+".config", rendered, schemas[name].Config)
		if err != nil {
			return err
		}
		spec.Config = merged
		cfg.Plugins[name] = spec
	}
	return nil
}
