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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/cue/parser"

	"github.com/justenwalker/kevin/internal/uerr"
)

// CoreSchema is kevin's environment-file schema (schema.cue), exported so
// [internal/plugins/index] can compile it alongside its own definitions.
//
//go:embed schema.cue
var CoreSchema []byte

// FileNames are the unnamed environment filenames [Load] looks for in a
// project directory, in the order it looks for them. A named environment
// (see [Load]) looks for the same set with "<name>." prefixed to "kevin".
var FileNames = candidateFiles("")

// cueExt is the file extension of a CUE source file.
const cueExt = ".cue"

// candidateFiles builds the ordered list of filenames [Load] accepts for a
// given environment name ("" for the unnamed environment): a visible and a
// dotfile variant.
func candidateFiles(name string) []string {
	prefix := ""
	if name != "" {
		prefix = name + "."
	}
	return []string{prefix + "kevin.cue", "." + prefix + "kevin.cue"}
}

// File is an environment that kevin read but did not validate in full.
type File struct {
	ctx   *cue.Context
	value cue.Value
	dir   string
	name  string

	jsonOnce sync.Once
	json     []byte
	jsonErr  error

	docOnce sync.Once
	doc     *document
	docErr  error

	setupScope, envScope scopeCache
}

// scopeCache holds one scope's decoded entries.
type scopeCache struct {
	once    sync.Once
	entries scopeEntries
	err     error
}

// init panics at startup if the embedded schema.cue fails to compile.
//
//nolint:gochecknoinits
func init() {
	ctx := cuecontext.New()
	mustCompileCoreSchema(ctx)
}

func mustCompileCoreSchema(ctx *cue.Context) cue.Value {
	v := ctx.CompileBytes(CoreSchema, cue.Filename("kevin/schema.cue"))
	if err := v.Err(); err != nil {
		panic(fmt.Errorf("config: compile core schema: %w", err))
	}
	return v
}

// Load reads the environment file from dir and unifies it with the core
// schema. With name == "", Load looks for the unnamed environment
// (kevin.cue or .kevin.cue); otherwise it looks for the named one
// (<name>.kevin.cue or .<name>.kevin.cue). Exactly one candidate may exist.
//
// The candidate may declare a package clause. When it does, Load unifies
// every other .cue file in dir that shares the same package clause
// alongside it (CUE's own multi-file-per-package model, via
// [cuelang.org/go/cue/load]) - a .cue file in dir with a *different*
// package clause returns [ErrPackageConflict].
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

// loadUser parses path into a [cue.Value]. If path declares a package
// clause, loadUser unifies every other .cue file in dir sharing that same
// clause alongside it, injecting tags as "@tag" values. A package-less path
// parses alone, as [parseFile].
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
// behavior for a candidate that declares no CUE package. It rejects a
// non-empty tags ([ErrTagWithoutPackage], "@tag"
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
	return trimmed == "kevin.cue" || strings.HasSuffix(trimmed, ".kevin.cue")
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

// parseFile parses src as CUE into a [cue.Value].
func parseFile(ctx *cue.Context, path string, src []byte) (cue.Value, error) {
	v := ctx.CompileBytes(src, cue.Filename(path))
	if err := v.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return v, nil
}

// Dir is the project directory.
func (f *File) Dir() string { return f.dir }

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
