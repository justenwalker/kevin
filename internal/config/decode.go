package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	cueerrors "cuelang.org/go/cue/errors"
)

// Plugins reports the plugins to start. Call it before [File.Validate].
//
// Plugins returns [ErrReservedNamespace] for a key whose namespace is
// [IsReservedName], before any plugin process starts.
func (f *File) Plugins() (map[string]PluginSpec, error) {
	var out struct {
		Plugins map[string]PluginSpec `json:"plugins"`
	}
	if err := f.decode(&out); err != nil {
		return nil, err
	}
	if err := f.validatePluginKeys(out.Plugins); err != nil {
		return nil, err
	}
	return out.Plugins, nil
}

// StepPlugins reports the distinct plugin name that a step in either scope
// uses, including a step that only appears inside a group. Call
// StepPlugins before [File.Plugins] starts a plugin.
func (f *File) StepPlugins() ([]string, error) {
	seen := make(map[string]struct{})
	for _, scope := range []Scope{ScopeSetup, ScopeEnv} {
		entries, err := f.decodeScope(scope)
		if err != nil {
			return nil, err
		}
		for _, spec := range entries.Steps {
			seen[spec.Uses.Plugin] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ResolvePlugin checks that name maps to a valid plugin name.
//
// Returns [ErrUnknownPlugin] when the name is not a valid plugin name.
func ResolvePlugin(name string, plugins map[string]PluginSpec) error {
	if name == Builtin {
		return nil
	}
	if _, ok := plugins[name]; ok {
		return nil
	}
	available := make([]string, 0, len(plugins)+1)
	available = append(available, Builtin)
	for key := range plugins {
		available = append(available, key)
	}
	sort.Strings(available)
	return fmt.Errorf("names plugin %q, available: %s: %w", name, strings.Join(available, ", "), ErrUnknownPlugin)
}

// Config decodes the environment. Call Config after Validate.
func (f *File) Config() (*Config, error) {
	if err := f.validateConcrete(); err != nil {
		return nil, f.invalid(fmt.Errorf("%w: %w", ErrInvalid, err))
	}

	cfg := &Config{Dir: f.dir}
	// The validateConcrete above already proved f.value decodes cleanly, so
	// this cannot fail.
	_ = f.decode(cfg)
	if err := f.fillVariableTypes(cfg.Variables); err != nil {
		return nil, err
	}

	setupEntries, err := f.decodeScope(ScopeSetup)
	if err != nil {
		return nil, err
	}
	envEntries, err := f.decodeScope(ScopeEnv)
	if err != nil {
		return nil, err
	}
	cfg.Setup = setupEntries.Steps
	cfg.Env = envEntries.Steps
	cfg.groups = map[Scope]map[string]Group{
		ScopeSetup: setupEntries.Groups,
		ScopeEnv:   envEntries.Groups,
	}

	cfg.Name = SlugName(f.name)
	if cfg.Project == "" {
		cfg.Project = projectName(f.dir)
	}
	if cfg.Name != "" {
		cfg.Project += "-" + cfg.Name
	}
	return cfg, nil
}

// decode unmarshals the CUE value's JSON encoding into into. The encoding is
// computed once per File.
func (f *File) decode(into any) error {
	f.jsonOnce.Do(func() { f.json, f.jsonErr = f.marshalJSON() })
	data, err := f.json, f.jsonErr
	if err != nil {
		return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, err))
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("config: decode: %w", err)
	}
	return nil
}

// invalid creates a validation error.
func (f *File) invalid(err error) error {
	return &ValidationError{Err: err, Pos: cueerrors.Positions(err), cfg: &cueerrors.Config{Cwd: f.dir}}
}
