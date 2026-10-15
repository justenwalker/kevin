package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"

	"github.com/justenwalker/kevin/internal/config/expr"
)

// Validate checks the config file for validity against the given set of plugin schemas.
// Each step reference and every config block must be valid against the schemas that the started plugins published.
//
// It also checks that each plugin key is a plain name, and is not [IsReservedName].
//
// Validate checks that each step's 'uses' field parses, names a plugin that resolves (see [ResolvePlugin), and
// names a step type that the plugin offers.
//
// It returns the first error it encounters, or nil if no errors were found.
func (f *File) Validate(schemas map[string]PluginSchemas) error {
	plugins, err := f.Plugins()
	if err != nil {
		return err
	}

	setupEntries, err := f.decodeScope(ScopeSetup)
	if err != nil {
		return err
	}
	envEntries, err := f.decodeScope(ScopeEnv)
	if err != nil {
		return err
	}

	doc, err := f.document()
	if err != nil {
		return err
	}

	for _, scope := range []struct {
		name    Scope
		entries scopeEntries
	}{{ScopeSetup, setupEntries}, {ScopeEnv, envEntries}} {
		for step, spec := range scope.entries.Steps {
			if err := f.validateStep(scope.name, step, scope.entries.Paths[step], spec, plugins, schemas, doc.Variables); err != nil {
				return err
			}
		}
	}

	if err := f.validateNeeds(ScopeSetup, setupEntries, setupEntries); err != nil {
		return err
	}
	if err := f.validateNeeds(ScopeEnv, envEntries, setupEntries); err != nil {
		return err
	}

	for name, cmd := range doc.Commands {
		if err := f.validateCommand(name, cmd, envEntries.Steps, setupEntries.Steps, schemas, doc.Variables); err != nil {
			return err
		}
	}

	return f.validatePluginConfigs(plugins, schemas, doc.Variables)
}

// validatePluginKeys checks that every plugins key is not [IsReservedName]. The
// core schema already restricts a key to a plain plugin name.
func (f *File) validatePluginKeys(plugins map[string]PluginSpec) error {
	for key := range plugins {
		if IsReservedName(key) {
			err := fmt.Errorf("config: plugins.%s: %s is a reserved name, reserved names are %s: %w",
				key, key, strings.Join(reservedNames, ", "), ErrReservedNamespace)
			pos := f.value.LookupPath(cue.MakePath(cue.Str("plugins"), cue.Str(key))).Pos()
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}
	}
	return nil
}

// compileSchema compiles a CUE schema that declares #Config. compileSchema
// returns false when src is empty, and the caller then accepts any value.
func (f *File) compileSchema(label string, src []byte) (cue.Value, bool, error) {
	if len(src) == 0 {
		return cue.Value{}, false, nil
	}
	v := f.ctx.CompileBytes(src, cue.Filename(label+"/schema.cue"))
	if err := v.Err(); err != nil {
		wrapped := cueerrors.Wrapf(err, v.Pos(), "%s schema", label)
		return cue.Value{}, false, f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
	}
	cfg := v.LookupPath(cue.ParsePath("#Config"))
	if !cfg.Exists() {
		err := fmt.Errorf("config: %s schema declares no #Config: %w", label, ErrInvalid)
		return cue.Value{}, false, f.invalid(cueerrors.Wrapf(err, v.Pos(), ""))
	}
	return cfg, true, nil
}

// validateStep checks that a step names a step type that resolves, then
// unifies the step's with block against the schema of that step type. path
// is the step's own CUE source path - a plain step's own key, or a
// flattened group member's "<scope>.<group>.steps.<member>" path.
func (f *File) validateStep(scopeName Scope, step string, path cue.Path, spec Step, plugins map[string]PluginSpec, schemas map[string]PluginSchemas, variables map[string]Variable) error {
	pos := f.value.LookupPath(path.Append(cue.Str("uses"))).Pos()

	ref := spec.Uses
	if watchErr := f.validateWatch(scopeName, step, path, spec.Watch); watchErr != nil {
		return watchErr
	}
	if resolveErr := ResolvePlugin(ref.Plugin, plugins); resolveErr != nil {
		return f.invalid(cueerrors.Wrapf(fmt.Errorf("config: %s.%s: %w", scopeName, step, resolveErr), pos, ""))
	}

	steps := schemas[ref.Plugin].Steps
	src, offered := steps[ref.Step]
	if !offered {
		names := make([]string, 0, len(steps))
		for name := range steps {
			names = append(names, name)
		}
		sort.Strings(names)
		stepErr := fmt.Errorf("config: %s.%s: plugin %q offers no step type %q, available: %s: %w",
			scopeName, step, ref.Plugin, ref.Step, strings.Join(names, ", "), ErrUnknownStepType)
		return f.invalid(cueerrors.Wrapf(stepErr, pos, ""))
	}

	with := f.value.LookupPath(path.Append(cue.Str("with")))
	if !with.Exists() {
		with = f.ctx.CompileString("{}")
	}

	tpl, tplErr := spec.WithTemplate()
	if tplErr != nil {
		return f.invalid(cueerrors.Wrapf(fmt.Errorf("config: %s.%s.with: %w", scopeName, step, tplErr), pos, ""))
	}
	if refErr := validateNeedsReferences(scopeName.String(), step, "with", spec.Needs, tpl); refErr != nil {
		// pos (the step's "uses" field), not with.Pos(): a "with" value that
		// exists only via the core schema's own optional "with?: {...}"
		// declaration reports schema.cue's position, not the actual file's.
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
	}
	if refErr := validateVarReferences(scopeName.String(), step, "with", variables, tpl); refErr != nil {
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
	}

	schema, ok, err := f.compileSchema(ref.String(), src)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	merged, except, err := unifyDeferred(f.ctx, schema, with, spec.With)
	if err != nil {
		return err
	}
	if err := merged.Err(); err != nil {
		wrapped := cueerrors.Wrapf(err, with.Pos(), "%s.%s.with", scopeName, step)
		return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
	}
	if err := concreteExcept(merged, cue.Path{}, except); err != nil {
		wrapped := cueerrors.Wrapf(err, with.Pos(), "%s.%s.with", scopeName, step)
		return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
	}
	return nil
}

// validateWatch checks a step's watch paths: only an env step may watch, and
// each path must be relative, stay inside the project directory, and exist.
func (f *File) validateWatch(scopeName Scope, step string, path cue.Path, watch []string) error {
	if len(watch) == 0 {
		return nil
	}
	at := path.Append(cue.Str("watch"))
	if scopeName != ScopeEnv {
		return f.wrapAt(at, fmt.Errorf("config: %s.%s.watch: only env steps can watch: %w", scopeName, step, ErrBadWatch))
	}
	for _, p := range watch {
		if filepath.IsAbs(p) {
			return f.wrapAt(at, fmt.Errorf("config: %s.%s.watch %q: path must be relative: %w", scopeName, step, p, ErrBadWatch))
		}
		clean := filepath.Clean(p)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return f.wrapAt(at, fmt.Errorf("config: %s.%s.watch %q: path escapes the project directory: %w", scopeName, step, p, ErrBadWatch))
		}
		if _, err := os.Stat(filepath.Join(f.dir, clean)); err != nil {
			reason := err.Error()
			if errors.Is(err, fs.ErrNotExist) {
				reason = "path does not exist"
			}
			return f.wrapAt(at, fmt.Errorf("config: %s.%s.watch %q: %s: %w", scopeName, step, p, reason, ErrBadWatch))
		}
	}
	return nil
}

// parseTemplate parses raw, naming the block in an error.
func parseTemplate(scopeName, step, field string, raw json.RawMessage) (*expr.Template, error) {
	tpl, err := expr.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s.%s.%s: %w", scopeName, step, field, err)
	}
	return tpl, nil
}

// validateNeedsReferences checks that every "needs.<step>..." and
// "setup.<step>..." reference inside raw's "${...}" markers names a step
// that needs actually declares - the same rule internal/config/expr's renderer
// enforces at Up time ("no such key"), caught here statically instead: both
// facts (which steps a block references, and which steps a needs list
// declares) already sit in the parsed file, no plugin process or Docker
// resource required to check them against each other. field names the
// block in an error message ("with" for a step, "run" for a command).
func validateNeedsReferences(scopeName string, step, field string, needs []Need, tpl *expr.Template) error {
	needsRefs, setupRefs := tpl.Refs().Needs, tpl.Refs().Setup

	declaredNeeds := make(map[string]struct{}, len(needs))
	declaredSetup := make(map[string]struct{}, len(needs))
	for _, n := range needs {
		if n.Setup {
			declaredSetup[n.Step] = struct{}{}
			continue
		}
		declaredNeeds[n.Step] = struct{}{}
	}

	for _, name := range needsRefs {
		if _, ok := declaredNeeds[name]; !ok {
			return fmt.Errorf(
				"config: %s.%s.%s: references %q via needs.%s, but %q is not in %s.%s's needs list: %w",
				scopeName, step, field, name, name, name, scopeName, step, ErrUndeclaredNeed)
		}
	}
	for _, name := range setupRefs {
		if _, ok := declaredSetup[name]; !ok {
			return fmt.Errorf(
				"config: %s.%s.%s: references %q via setup.%s, but %q is not in %s.%s's needs list as %q: %w",
				scopeName, step, field, name, name, name, scopeName, step, Need{Setup: true, Step: name}.String(), ErrUndeclaredNeed)
		}
	}
	return nil
}

// validateCommand checks that every entry in cmd's needs list names a real
// step - a plain name in env, a "setup.<name>" entry in setup - and that the
// step's plugin implements Export for that step's type, so a step that
// can't export fails here, before any plugin process runs Up or Export,
// rather than at "kevin do" time. It also checks that every
// "${needs.<step>...}"/"${setup.<step>...}" reference inside cmd.Run names
// a step cmd.Needs actually declares, the same static check a step's with
// block gets.
func (f *File) validateCommand(name string, cmd Command, env, setup map[string]Step, schemas map[string]PluginSchemas, variables map[string]Variable) error {
	pos := f.value.LookupPath(cue.MakePath(cue.Str("commands"), cue.Str(name), cue.Str("needs"))).Pos()

	if err := validateNeedsSyntax(cmd.Needs); err != nil {
		return f.invalid(cueerrors.Wrapf(fmt.Errorf("config: commands.%s.needs: %w", name, err), pos, ""))
	}

	for _, n := range cmd.Needs {
		scopeName, scope, stepName := ScopeEnv, env, n.Step
		if n.Setup {
			scopeName, scope, stepName = ScopeSetup, setup, n.Step
		}
		step, ok := scope[stepName]
		if !ok {
			err := fmt.Errorf("config: commands.%s.needs: %q names no step in scope %q: %w",
				name, n.String(), scopeName, ErrUnknownStep)
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}
		ref := step.Uses
		if !schemas[ref.Plugin].Export[ref.Step] {
			err := fmt.Errorf("config: commands.%s.needs: step %q (%s) does not implement export: %w",
				name, stepName, ref, ErrExportNotSupported)
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}
	}

	tpl, err := parseTemplate("commands", name, "run", cmd.Run)
	if err != nil {
		return f.invalid(cueerrors.Wrapf(err, pos, ""))
	}
	if refErr := validateNeedsReferences("commands", name, "run", cmd.Needs, tpl); refErr != nil {
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
	}
	if refErr := validateVarReferences("commands", name, "run", variables, tpl); refErr != nil {
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
	}
	return nil
}

// validatePluginConfigs checks the config block of every declared plugin
// against the config schema that plugin published. A plugin with no config
// block skips the check.
//
// A config block has no needs/setup scope at all - no step has run Up yet
// when Configure sends it (see [internal/plugins/load.Loaded.Configure]) - so a
// "${needs...}"/"${setup...}" reference inside one is always an error, not
// a deferred one the way a step's with block gets: this checks that with an
// empty needs list, the same way [validateNeedsReferences] checks a step's
// with block against its own declared needs.
func (f *File) validatePluginConfigs(plugins map[string]PluginSpec, schemas map[string]PluginSchemas, variables map[string]Variable) error {
	names := make([]string, 0, len(plugins))
	for name := range plugins {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		spec := plugins[name]
		if len(spec.Config) == 0 {
			continue
		}

		pos := f.value.LookupPath(cue.MakePath(cue.Str("plugins"), cue.Str(name), cue.Str("config"))).Pos()

		tpl, tplErr := parseTemplate("plugins", name, "config", spec.Config)
		if tplErr != nil {
			return f.invalid(cueerrors.Wrapf(tplErr, pos, ""))
		}
		if refErr := validateNeedsReferences("plugins", name, "config", nil, tpl); refErr != nil {
			return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
		}
		if refErr := validateVarReferences("plugins", name, "config", variables, tpl); refErr != nil {
			return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
		}

		configSrc := schemas[name].Config
		if len(configSrc) == 0 {
			err := fmt.Errorf("config: plugins.%s.config: plugin %q publishes no config schema: %w",
				name, name, ErrConfigNotSupported)
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}

		schema, _, err := f.compileSchema(name, configSrc)
		if err != nil {
			return err
		}

		// spec.Config above was decoded from this same path, so it exists.
		val := f.value.LookupPath(cue.MakePath(cue.Str("plugins"), cue.Str(name), cue.Str("config")))
		merged, except, err := unifyDeferred(f.ctx, schema, val, spec.Config)
		if err != nil {
			return err
		}
		if err := merged.Err(); err != nil {
			wrapped := cueerrors.Wrapf(err, val.Pos(), "plugins.%s.config", name)
			return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
		}
		if err := concreteExcept(merged, cue.Path{}, except); err != nil {
			wrapped := cueerrors.Wrapf(err, val.Pos(), "plugins.%s.config", name)
			return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
		}
	}
	return nil
}
