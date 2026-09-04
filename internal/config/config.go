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
	"cuelang.org/go/cue/load"
	"cuelang.org/go/cue/parser"
	jsonpkg "cuelang.org/go/encoding/json"
	yamlpkg "cuelang.org/go/encoding/yaml"

	"github.com/justenwalker/kevin/internal/expr"
	"github.com/justenwalker/kevin/internal/uerr"
)

//go:embed schema.cue
var coreSchema []byte

// FileNames are the unnamed environment filenames [Load] looks for in a
// project directory, in the order it looks for them. A named environment
// (see [Load]) looks for the same set with "<name>." prefixed to "kevin".
var FileNames = candidateFiles("")

// envFileExts are the file extensions [candidateFiles] and
// [looksLikeCandidateName] recognize as an environment file format.
var envFileExts = [...]string{"cue", "yaml", "yml", "json"}

// cueExt is the file extension of a CUE source file.
const cueExt = ".cue"

// candidateFiles builds the ordered list of filenames [Load] accepts for a
// given environment name ("" for the unnamed environment): a visible and a
// dotfile variant of each supported format.
func candidateFiles(name string) []string {
	prefix := ""
	if name != "" {
		prefix = name + "."
	}
	out := make([]string, 0, len(envFileExts)*2)
	for _, ext := range envFileExts {
		out = append(out, prefix+"kevin."+ext, "."+prefix+"kevin."+ext)
	}
	return out
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

// Command is one entry of the commands block, run on demand by name.
type Command struct {
	Needs []string        `json:"needs"`
	Run   json.RawMessage `json:"run"`
	Cwd   string          `json:"cwd"`
	Label string          `json:"label"`
}

// PluginSchemas are the schemas that one plugin publishes.
type PluginSchemas struct {
	// Config constrains the config block of the plugin. It is empty when the
	// plugin takes no configuration.
	Config []byte

	// Steps constrains the with block of each step type, by step name.
	Steps map[string][]byte

	// Export reports, by step name, whether that step type implements
	// Export.
	Export map[string]bool
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

	// Commands run on demand with "kevin do <name>".
	Commands map[string]Command `json:"commands"`

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
// JSON. Exactly one candidate may exist.
//
// A CUE candidate may declare a package clause. When it does, Load unifies
// every other .cue file in dir that shares the same package clause
// alongside it (CUE's own multi-file-per-package model, via
// [cuelang.org/go/cue/load]) - a .cue file in dir with a *different*
// package clause returns [ErrPackageConflict]. YAML and JSON candidates
// never declare a package and always load alone.
//
// tags injects "@tag" values (see [cuelang.org/go/cue/load.Config.Tags])
// into a package-mode load; a bare "name" entry (no "=value") is shorthand
// for "name=true". tags is only meaningful when the resolved candidate
// declares a package - a non-empty tags against a package-less candidate
// returns [ErrTagWithoutPackage].
func Load(dir, name string, tags []string) (*File, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("config: abs path %q: %w", dir, err)
	}

	path, err := findFile(abs, name)
	if err != nil {
		return nil, err
	}

	ctx := cuecontext.New()

	schema := mustCompileCoreSchema(ctx)

	user, err := loadUser(ctx, abs, path, tags)
	if err != nil {
		return nil, err
	}

	value := schema.Unify(user)
	if err = value.Err(); err != nil {
		return nil, newValidationError(abs, fmt.Errorf("%w: %w", ErrInvalid, err))
	}

	return &File{ctx: ctx, value: value, dir: abs, name: name}, nil
}

// loadUser parses path into a [cue.Value]. If path is a .cue file that
// declares a package clause, loadUser unifies every other .cue file in dir
// sharing that same clause alongside it, injecting tags as "@tag" values. A
// package-less path (including every YAML/JSON candidate, which cannot
// declare a package) parses alone, as [parseFile].
func loadUser(ctx *cue.Context, dir, path string, tags []string) (cue.Value, error) {
	pkg, err := cuePackageName(path)
	if err != nil {
		return cue.Value{}, err
	}

	if pkg == "" {
		return loadLegacyFile(ctx, dir, path, tags)
	}

	files, err := packageModeFiles(dir, path, pkg)
	if err != nil {
		return cue.Value{}, err
	}
	insts := load.Instances(files, &load.Config{Dir: dir, Package: pkg, Tags: normalizeTags(tags)})
	v := ctx.BuildInstance(insts[0])
	if err := v.Err(); err != nil {
		return cue.Value{}, newValidationError(dir, fmt.Errorf("%w: %w", ErrInvalid, err))
	}
	return v, nil
}

// loadLegacyFile parses path alone, as [parseFile] - the pre-package-mode
// behavior for a candidate (of any supported format) that declares no CUE
// package. It rejects a non-empty tags ([ErrTagWithoutPackage], "@tag"
// injection only applies to a package-mode load) and a directory that mixes
// this package-less candidate with a package-mode sibling
// ([ErrPackageConflict], see [packageModeSiblings]).
func loadLegacyFile(ctx *cue.Context, dir, path string, tags []string) (cue.Value, error) {
	if len(tags) > 0 {
		return cue.Value{}, uerr.Wrap(fmt.Errorf("config: %s: %w", path, ErrTagWithoutPackage),
			"-t/--tag was set but %s declares no CUE package - add \"package <name>\" to use --tag", path)
	}
	siblings, err := packageModeSiblings(dir, path)
	if err != nil {
		return cue.Value{}, err
	}
	if len(siblings) > 0 {
		return cue.Value{}, uerr.Wrap(
			fmt.Errorf("config: %s: found package clauses in %s: %w", path, strings.Join(siblings, ", "), ErrPackageConflict),
			"%s declares no CUE package, but %s in the same directory does - "+
				"give %s a matching \"package\" clause, or remove the stray file",
			path, strings.Join(siblings, ", "), path)
	}
	src, err := os.ReadFile(path) //nolint:gosec // reading the project's own config file is the point
	if err != nil {
		return cue.Value{}, fmt.Errorf("config: read %q: %w", path, err)
	}
	v, err := parseFile(ctx, path, src)
	if err != nil {
		return cue.Value{}, newValidationError(dir, fmt.Errorf("%w: %w", ErrInvalid, err))
	}
	return v, nil
}

// cuePackageName reports the CUE package clause path declares, or "" if
// path is not a .cue file, or is a .cue file with no package clause. It
// parses only the package clause, not the rest of the file.
func cuePackageName(path string) (string, error) {
	if filepath.Ext(path) != cueExt {
		return "", nil
	}
	f, err := parser.ParseFile(path, nil, parser.PackageClauseOnly)
	if err != nil {
		return "", fmt.Errorf("config: parse %q: %w", path, err)
	}
	return f.PackageName(), nil
}

// packageModeSiblings lists every other .cue file in dir (excluding path,
// and excluding any file [looksLikeCandidateName] recognizes as a different
// named or unnamed environment's own file) that declares a non-empty
// package clause. It only runs against a package-less path - package-mode
// loading resolves its own file list via [packageModeFiles] and needs no
// separate sibling scan.
func packageModeSiblings(dir, path string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", dir, err)
	}
	var found []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != cueExt || looksLikeCandidateName(e.Name()) {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if candidate == path {
			continue
		}
		pkg, err := cuePackageName(candidate)
		if err != nil {
			return nil, err
		}
		if pkg != "" {
			found = append(found, e.Name())
		}
	}
	sort.Strings(found)
	return found, nil
}

// packageModeFiles lists the .cue files [loadUser]'s package-mode branch
// unifies for path: path itself, plus every other .cue file in dir that
// declares pkg as its own package clause - excluding any file
// [looksLikeCandidateName] recognizes as a different named or unnamed
// environment's own file, so two environments sharing a directory (today's
// sibling-file named-environment convention, kevin.cue next to
// staging.kevin.cue) never merge into each other just because they happen
// to share a package name.
func packageModeFiles(dir, path, pkg string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", dir, err)
	}
	files := []string{path}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != cueExt || looksLikeCandidateName(e.Name()) {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		if candidate == path {
			continue
		}
		p, err := cuePackageName(candidate)
		if err != nil {
			return nil, err
		}
		if p == pkg {
			files = append(files, candidate)
		}
	}
	sort.Strings(files[1:])
	return files, nil
}

// looksLikeCandidateName reports whether base (a filename, not a full path)
// matches the "kevin.<ext>" naming grammar [candidateFiles] generates for
// any environment name - the unnamed environment, or a named one - so a
// package-mode load and its conflict check both know to leave a sibling
// environment's own file alone, whatever package clause it declares.
func looksLikeCandidateName(base string) bool {
	trimmed := strings.TrimPrefix(base, ".")
	for _, ext := range envFileExts {
		suffix := "kevin." + ext
		if trimmed == suffix || strings.HasSuffix(trimmed, "."+suffix) {
			return true
		}
	}
	return false
}

// normalizeTags translates a bare "name" entry (no "=value") into
// "name=true" for [cuelang.org/go/cue/load.Config.Tags]. A "name=value"
// entry passes through unchanged.
func normalizeTags(tags []string) []string {
	out := make([]string, len(tags))
	for i, t := range tags {
		if strings.Contains(t, "=") {
			out[i] = t
			continue
		}
		out[i] = t + "=true"
	}
	return out
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
				pos := f.value.LookupPath(cue.MakePath(cue.Str(scope.name), cue.Str(step), cue.Str("uses"))).Pos()
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
		Setup    map[string]Step    `json:"setup"`
		Env      map[string]Step    `json:"env"`
		Commands map[string]Command `json:"commands"`
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

	for name, cmd := range out.Commands {
		if err := f.validateCommand(name, cmd, out.Env, out.Setup, schemas); err != nil {
			return err
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
// unifies the step's with block against the schema of that step type.
func (f *File) validateStep(scopeName, step string, spec Step, plugins map[string]PluginSpec, schemas map[string]PluginSchemas) error {
	pos := f.value.LookupPath(cue.MakePath(cue.Str(scopeName), cue.Str(step), cue.Str("uses"))).Pos()

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

	with := f.value.LookupPath(cue.MakePath(cue.Str(scopeName), cue.Str(step), cue.Str("with")))
	if !with.Exists() {
		with = f.ctx.CompileString("{}")
	}

	if refErr := validateNeedsReferences(scopeName, step, "with", spec.Needs, spec.With); refErr != nil {
		// pos (the step's "uses" field), not with.Pos(): a "with" value that
		// exists only via the core schema's own optional "with?: {...}"
		// declaration reports schema.cue's position, not the actual file's.
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
	}

	schema, ok, err := f.compileSchema(ref.String(), src)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	merged := schema.Unify(with)
	if err := merged.Validate(cue.Concrete(true)); err != nil {
		wrapped := cueerrors.Wrapf(err, with.Pos(), "%s.%s.with", scopeName, step)
		return f.invalid(fmt.Errorf("%w: %w", ErrInvalid, wrapped))
	}
	return nil
}

// validateNeedsReferences checks that every "needs.<step>..." and
// "setup.<step>..." reference inside raw's "${...}" markers names a step
// that needs actually declares - the same rule internal/expr's renderer
// enforces at Up time ("no such key"), caught here statically instead: both
// facts (which steps a block references, and which steps a needs list
// declares) already sit in the parsed file, no plugin process or Docker
// resource required to check them against each other. field names the
// block in an error message ("with" for a step, "run" for a command).
func validateNeedsReferences(scopeName, step, field string, needs []string, raw json.RawMessage) error {
	needsRefs, setupRefs, err := expr.ReferencedSteps(raw)
	if err != nil {
		return fmt.Errorf("config: %s.%s.%s: %w", scopeName, step, field, err)
	}

	declaredNeeds := make(map[string]struct{}, len(needs))
	declaredSetup := make(map[string]struct{}, len(needs))
	for _, n := range needs {
		if rest, ok := strings.CutPrefix(n, "setup."); ok {
			declaredSetup[rest] = struct{}{}
			continue
		}
		declaredNeeds[n] = struct{}{}
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
				scopeName, step, field, name, name, name, scopeName, step, "setup."+name, ErrUndeclaredNeed)
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
func (f *File) validateCommand(name string, cmd Command, env, setup map[string]Step, schemas map[string]PluginSchemas) error {
	pos := f.value.LookupPath(cue.MakePath(cue.Str("commands"), cue.Str(name), cue.Str("needs"))).Pos()

	for _, n := range cmd.Needs {
		scopeName, scope, stepName := ScopeEnv, env, n
		if rest, ok := strings.CutPrefix(n, "setup."); ok {
			scopeName, scope, stepName = ScopeSetup, setup, rest
		}
		step, ok := scope[stepName]
		if !ok {
			err := fmt.Errorf("config: commands.%s.needs: %q names no step in scope %q: %w",
				name, n, scopeName, ErrUnknownStep)
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}
		ref, err := ParseStepRef(step.Uses)
		if err != nil {
			return f.invalid(cueerrors.Wrapf(fmt.Errorf("config: commands.%s.needs: %w", name, err), pos, ""))
		}
		if !schemas[ref.Plugin].Export[ref.Step] {
			err := fmt.Errorf("config: commands.%s.needs: step %q (%s) does not implement export: %w",
				name, stepName, ref, ErrExportNotSupported)
			return f.invalid(cueerrors.Wrapf(err, pos, ""))
		}
	}

	if refErr := validateNeedsReferences("commands", name, "run", cmd.Needs, cmd.Run); refErr != nil {
		return f.invalid(cueerrors.Wrapf(refErr, pos, ""))
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

		pos := f.value.LookupPath(cue.MakePath(cue.Str("plugins"), cue.Str(name), cue.Str("config"))).Pos()

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
