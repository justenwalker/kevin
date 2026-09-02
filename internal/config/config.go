// Package config loads and validates a kevin environment definition.
//
// Call the stages in order:
//
//  1. [Load] reads the environment file and unifies it with the core schema.
//  2. [File.Plugins] reports the plugin binaries to start.
//  3. [File.Validate] takes the schema of each started plugin and checks every
//     step against it.
//  4. [File.Config] decodes the result.
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	jsonpkg "cuelang.org/go/encoding/json"
	yamlpkg "cuelang.org/go/encoding/yaml"

	"github.com/justenwalker/kevin/internal/uerr"
)

//go:embed schema.cue
var coreSchema []byte

// FileNames are the unnamed environment filenames [Load] looks for in a
// project directory, in the order it looks for them. A named environment
// (see [Load]) looks for the same set with "<name>." prefixed to "kevin".
var FileNames = candidateFiles("")

// candidateFiles builds the ordered list of filenames [Load] accepts for a
// given environment name ("" for the unnamed environment): a visible and a
// dotfile variant of each supported format.
func candidateFiles(name string) []string {
	prefix := ""
	if name != "" {
		prefix = name + "."
	}
	exts := [...]string{"cue", "yaml", "yml", "json"}
	out := make([]string, 0, len(exts)*2)
	for _, ext := range exts {
		out = append(out, prefix+"kevin."+ext, "."+prefix+"kevin."+ext)
	}
	return out
}

// candidateLocalFiles builds the ordered list of local override filenames
// [Load] accepts for a given environment name, mirroring candidateFiles one
// step further: a visible and a dotfile variant, CUE only.
func candidateLocalFiles(name string) []string {
	prefix := ""
	if name != "" {
		prefix = name + "."
	}
	return []string{prefix + "kevin.local.cue", "." + prefix + "kevin.local.cue"}
}

// Builtin is the plugin name that resolves to kevin's builtin step types.
const Builtin = "builtin"

// PluginSpec is one entry in the plugins block.
type PluginSpec struct {
	Cmd    string            `json:"cmd"`
	Args   []string          `json:"args"`
	Env    map[string]string `json:"env"`
	Config json.RawMessage   `json:"config"`

	OCI      string `json:"oci"`
	File     string `json:"file"`
	Checksum string `json:"checksum"`
	HTTP     string `json:"http"`
	Signed   bool   `json:"signed"`
}

// Step is one node of the DAG.
type Step struct {
	Uses  string          `json:"uses"`
	Needs []string        `json:"needs"`
	With  json.RawMessage `json:"with"`

	// Label is a friendly display name for the console. Empty means the
	// step's own key names it instead.
	Label string `json:"label"`
}

// PluginSchemas are the schemas that one plugin publishes.
type PluginSchemas struct {
	// Config constrains the config block of the plugin. It is empty when the
	// plugin takes no configuration.
	Config []byte

	// Steps constrains the with block of each step type, by step name.
	Steps map[string][]byte
}

// Proxy configures the kevin proxy.
type Proxy struct {
	Listen      string      `json:"listen"`
	GatewayPort int         `json:"gateway_port"`
	Egress      ProxyEgress `json:"egress"`
}

// ProxyEgress configures which domains the proxy allows outbound traffic to.
type ProxyEgress struct {
	Allow []string `json:"allow"`
	Deny  bool     `json:"deny"`
}

// Console configures the web console.
type Console struct {
	Listen string `json:"listen"`
}

// Relay configures the in-network relay.
type Relay struct {
	Image string `json:"image"`
}

// Config is a valid environment.
type Config struct {
	// Dir is the project directory that the environment came from.
	Dir string `json:"-"`

	Project string                `json:"project"`
	Domain  string                `json:"domain"`
	Plugins map[string]PluginSpec `json:"plugins"`

	// Name is the resolved, slugified environment name Load was given
	// (see [Load]), "" for the unnamed environment.
	Name string `json:"-"`

	// Setup steps persist across runs.
	Setup map[string]Step `json:"setup"`

	// Env steps are ephemeral.
	Env map[string]Step `json:"env"`

	Proxy   Proxy   `json:"proxy"`
	Console Console `json:"console"`
	Relay   Relay   `json:"relay"`
}

// Steps returns the steps of one scope. The scope is "setup" or "env".
func (c *Config) Steps(scope string) map[string]Step {
	if scope == ScopeSetup {
		return c.Setup
	}
	return c.Env
}

// Scopes are the two independent DAGs in an environment.
const (
	ScopeSetup = "setup"
	ScopeEnv   = "env"
)

// File is an environment that kevin read but did not validate in full.
type File struct {
	ctx   *cue.Context
	value cue.Value
	dir   string
	name  string
}

// init panics at startup if the embedded schema.cue fails to compile.
//
//nolint:gochecknoinits
func init() {
	ctx := cuecontext.New()
	mustCompileCoreSchema(ctx)
}

func mustCompileCoreSchema(ctx *cue.Context) cue.Value {
	v := ctx.CompileBytes(coreSchema, cue.Filename("kevin/schema.cue"))
	if err := v.Err(); err != nil {
		panic(fmt.Errorf("config: compile core schema: %w", err))
	}
	return v
}

// Load reads the environment file from dir and unifies it with the core
// schema. With name == "", Load looks for the unnamed environment
// (kevin.<ext> or .kevin.<ext>); otherwise it looks for the named one
// (<name>.kevin.<ext> or .<name>.kevin.<ext>) for each of CUE, YAML, and
// JSON. Exactly one candidate may exist. The file must not declare a CUE
// package clause.
//
// Load then looks for an optional local override file (kevin.local.cue or
// .kevin.local.cue, prefixed with "<name>." when named) and unifies it onto
// the result if present. A field the override should be able to replace
// must be declared in the base file with a CUE default (field: *"x" | _);
// unifying two different concrete values for the same field is an error.
func Load(dir, name string) (*File, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("config: abs path %q: %w", dir, err)
	}

	path, err := findFile(abs, name)
	if err != nil {
		return nil, err
	}

	src, err := os.ReadFile(path) //nolint:gosec // reading the project's own config file is the point
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	ctx := cuecontext.New()

	schema := mustCompileCoreSchema(ctx)

	user, err := parseFile(ctx, path, src)
	if err != nil {
		return nil, newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, err))
	}

	value := schema.Unify(user)
	if err = value.Err(); err != nil {
		return nil, newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, err))
	}

	localPath, err := findLocalFile(abs, name)
	if err != nil {
		return nil, err
	}
	if localPath != "" {
		localSrc, err := os.ReadFile(localPath) //nolint:gosec // reading the project's own config file is the point
		if err != nil {
			return nil, fmt.Errorf("config: read %q: %w", localPath, err)
		}
		local := ctx.CompileBytes(localSrc, cue.Filename(localPath))
		if err := local.Err(); err != nil {
			return nil, newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, err))
		}
		value = value.Unify(local)
		if err := value.Err(); err != nil {
			return nil, newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, err))
		}
	}

	return &File{ctx: ctx, value: value, dir: abs, name: name}, nil
}

// findFile locates the single environment file for name in abs, among
// candidateFiles(name). It returns [ErrNotFound] when none exist, and
// [ErrAmbiguous] when more than one does.
func findFile(abs, name string) (string, error) {
	var found []string
	for _, candidate := range candidateFiles(name) {
		path := filepath.Join(abs, candidate)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return "", uerr.Wrap(fmt.Errorf("config: %s: %w", abs, ErrNotFound),
			"no kevin.cue found in %s (or a matching name) - create one, or pass -C to point at a different directory", abs)
	case 1:
		return found[0], nil
	default:
		return "", uerr.Wrap(fmt.Errorf("config: %s: found %s: %w", abs, strings.Join(found, ", "), ErrAmbiguous),
			"multiple environment files found in %s (%s) - keep one, or pass -e to pick a named environment",
			abs, strings.Join(found, ", "))
	}
}

// findLocalFile locates the optional local override file for name in abs,
// among candidateLocalFiles(name). It returns ("", nil) when none exist, and
// [ErrAmbiguous] when more than one does.
func findLocalFile(abs, name string) (string, error) {
	var found []string
	for _, candidate := range candidateLocalFiles(name) {
		path := filepath.Join(abs, candidate)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", uerr.Wrap(fmt.Errorf("config: %s: found %s: %w", abs, strings.Join(found, ", "), ErrAmbiguous),
			"multiple local override files found in %s (%s) - keep one",
			abs, strings.Join(found, ", "))
	}
}

// parseFile parses src as CUE, YAML, or JSON, chosen by path's extension,
// into a [cue.Value].
func parseFile(ctx *cue.Context, path string, src []byte) (cue.Value, error) {
	var v cue.Value
	switch filepath.Ext(path) {
	case ".yaml", ".yml":
		astFile, err := yamlpkg.Extract(path, src)
		if err != nil {
			return cue.Value{}, fmt.Errorf("config: parse %q: %w", path, err)
		}
		v = ctx.BuildFile(astFile)
	case ".json":
		expr, err := jsonpkg.Extract(path, src)
		if err != nil {
			return cue.Value{}, fmt.Errorf("config: parse %q: %w", path, err)
		}
		v = ctx.BuildExpr(expr)
	default:
		v = ctx.CompileBytes(src, cue.Filename(path))
	}
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return v, nil
}

// Dir is the project directory.
func (f *File) Dir() string { return f.dir }

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
// uses. Call StepPlugins before [File.Plugins] starts a plugin.
func (f *File) StepPlugins() ([]string, error) {
	var out struct {
		Setup map[string]Step `json:"setup"`
		Env   map[string]Step `json:"env"`
	}
	if err := f.decode(&out); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	for _, scope := range []struct {
		name  string
		steps map[string]Step
	}{{ScopeSetup, out.Setup}, {ScopeEnv, out.Env}} {
		for step, spec := range scope.steps {
			ref, err := ParseStepRef(spec.Uses)
			if err != nil {
				pos := f.value.LookupPath(cue.ParsePath(scope.name + "." + step + ".uses")).Pos()
				return nil, f.invalid(cueerrors.Wrapf(fmt.Errorf("config: step names step type %q: %w", spec.Uses, err), pos, ""))
			}
			seen[ref.Plugin] = struct{}{}
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

	var out struct {
		Setup map[string]Step `json:"setup"`
		Env   map[string]Step `json:"env"`
	}
	// f.Plugins above already decoded f.value once and succeeded, so this
	// decode of the same value cannot fail.
	_ = f.decode(&out)

	for _, scope := range []struct {
		name  string
		steps map[string]Step
	}{{ScopeSetup, out.Setup}, {ScopeEnv, out.Env}} {
		for step, spec := range scope.steps {
			if err := f.validateStep(scope.name, step, spec, plugins, schemas); err != nil {
				return err
			}
		}
	}

	return f.validatePluginConfigs(plugins, schemas)
}

// validatePluginKeys checks that every plugins key is not [IsReservedName]. The
// core schema already restricts a key to a plain plugin name.
func (f *File) validatePluginKeys(plugins map[string]PluginSpec) error {
	for key := range plugins {
		if IsReservedName(key) {
			err := fmt.Errorf("config: plugins.%s: %s is a reserved name, reserved names are %s: %w",
				key, key, strings.Join(reservedNames, ", "), ErrReservedNamespace)
			pos := f.value.LookupPath(cue.ParsePath("plugins." + key)).Pos()
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
// unifies the step's with block against the schema of that step type.
func (f *File) validateStep(scopeName, step string, spec Step, plugins map[string]PluginSpec, schemas map[string]PluginSchemas) error {
	pos := f.value.LookupPath(cue.ParsePath(scopeName + "." + step + ".uses")).Pos()

	ref, err := ParseStepRef(spec.Uses)
	if err != nil {
		return f.invalid(cueerrors.Wrapf(fmt.Errorf("config: %s.%s.uses: %w", scopeName, step, err), pos, ""))
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

	schema, ok, err := f.compileSchema(ref.String(), src)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	with := f.value.LookupPath(cue.ParsePath(scopeName + "." + step + ".with"))
	if !with.Exists() {
		with = f.ctx.CompileString("{}")
	}
	merged := schema.Unify(with)
	if err := merged.Validate(cue.Concrete(true)); err != nil {
		wrapped := cueerrors.Wrapf(err, with.Pos(), "%s.%s.with", scopeName, step)
		return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
	}
	return nil
}

// validatePluginConfigs checks the config block of every declared plugin
// against the config schema that plugin published. A plugin with no config
// block skips the check.
func (f *File) validatePluginConfigs(plugins map[string]PluginSpec, schemas map[string]PluginSchemas) error {
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

		pos := f.value.LookupPath(cue.ParsePath("plugins." + name + ".config")).Pos()

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
		val := f.value.LookupPath(cue.ParsePath("plugins." + name + ".config"))
		merged := schema.Unify(val)
		if err := merged.Validate(cue.Concrete(true)); err != nil {
			wrapped := cueerrors.Wrapf(err, val.Pos(), "plugins.%s.config", name)
			return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
		}
	}
	return nil
}

// Config decodes the environment. Call Config after Validate.
func (f *File) Config() (*Config, error) {
	if err := f.value.Validate(cue.Concrete(true)); err != nil {
		return nil, f.invalid(fmt.Errorf("%w: %w", ErrInvalid, err))
	}

	cfg := &Config{Dir: f.dir}
	// The Validate(cue.Concrete(true)) above already proved f.value decodes
	// cleanly, so this cannot fail.
	_ = f.decode(cfg)
	cfg.Name = SlugName(f.name)
	if cfg.Project == "" {
		cfg.Project = projectName(f.dir)
		if cfg.Name != "" {
			cfg.Project += "-" + cfg.Name
		}
	}
	return cfg, nil
}

// decode renders the CUE value as JSON, then unmarshals the JSON into into.
func (f *File) decode(into any) error {
	data, err := f.value.MarshalJSON()
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

// projectName builds a resource-safe default project name from the project
// directory. An empty or all-punctuation base name becomes "kevin".
func projectName(dir string) string {
	name := SlugName(filepath.Base(dir))
	if name == "" {
		return "kevin"
	}
	return name
}

// SlugName lowercases s and turns every run of characters that are not a
// lowercase letter or digit into a single hyphen, trimming leading and
// trailing hyphens. It is used to build resource-safe names (Docker
// networks, filesystem paths) from arbitrary strings such as a directory
// name or a Load environment name.
func SlugName(s string) string {
	base := strings.ToLower(s)
	var b strings.Builder
	for _, r := range base {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if bs := b.String(); bs != "" && !strings.HasSuffix(bs, "-") {
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
