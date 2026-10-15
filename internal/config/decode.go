package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strings"

	cueerrors "cuelang.org/go/cue/errors"
)

// Plugins reports the plugins to start. Call it before [File.Validate].
//
// Plugins returns [ErrReservedNamespace] for a key whose namespace is
// [IsReservedName], before any plugin process starts.
func (f *File) Plugins() (map[string]PluginSpec, error) {
	doc, err := f.document()
	if err != nil {
		return nil, err
	}
	if err := f.validatePluginKeys(doc.Plugins); err != nil {
		return nil, err
	}
	return maps.Clone(doc.Plugins), nil
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

	doc, err := f.document()
	if err != nil {
		return nil, err
	}
	cfg := new(Config)
	*cfg = doc.Config
	cfg.Dir = f.dir
	cfg.Plugins = maps.Clone(doc.Plugins)
	cfg.Commands = maps.Clone(doc.Commands)
	cfg.Variables = maps.Clone(doc.Variables)
	if err = f.fillVariableTypes(cfg.Variables); err != nil {
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
	cfg.Setup = maps.Clone(setupEntries.Steps)
	cfg.Env = maps.Clone(envEntries.Steps)
	cfg.groups = map[Scope]map[string]Group{
		ScopeSetup: maps.Clone(setupEntries.Groups),
		ScopeEnv:   maps.Clone(envEntries.Groups),
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

// document is the whole environment, decoded once per File. Its scope blocks
// stay raw: [File.decodeScope] turns each into steps and groups.
type document struct {
	Config

	Setup map[string]json.RawMessage `json:"setup"`
	Env   map[string]json.RawMessage `json:"env"`
}

// document decodes the whole environment once per File. Callers must not
// change what it returns.
func (f *File) document() (*document, error) {
	f.docOnce.Do(func() {
		doc := new(document)
		if err := f.decode(doc); err != nil {
			f.docErr = err
			return
		}
		f.doc = doc
	})
	return f.doc, f.docErr
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
