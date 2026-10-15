package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"cuelang.org/go/cue"
)

// setupNeedPrefix marks a needs entry as naming a setup-scope step.
const setupNeedPrefix = "setup."

// Need is one entry of a needs list.
type Need struct {
	// Setup is true for a "setup.<name>" entry: a setup-scope step named from
	// an env step. Otherwise the entry names a step or group in the scope of
	// the list's owner.
	Setup bool

	// Step is the step or group name, without any "setup." prefix.
	Step string
}

// ParseNeed parses one needs entry. It is the only code that knows the
// "setup." prefix.
func ParseNeed(s string) Need {
	if rest, ok := strings.CutPrefix(s, setupNeedPrefix); ok {
		return Need{Setup: true, Step: rest}
	}
	return Need{Step: s}
}

// String returns the entry as written in kevin.cue.
func (n Need) String() string {
	if n.Setup {
		return setupNeedPrefix + n.Step
	}
	return n.Step
}

// UnmarshalText decodes a needs entry from its kevin.cue form.
func (n *Need) UnmarshalText(text []byte) error {
	*n = ParseNeed(string(text))
	return nil
}

// NeedStrings returns the entries in their kevin.cue form.
func NeedStrings(needs []Need) []string {
	out := make([]string, len(needs))
	for i, n := range needs {
		out[i] = n.String()
	}
	return out
}

// validateNeeds checks that every needs entry of scope's steps names a real
// step or group: in the same scope, or, for an env step only, a
// "setup.<name>" entry in the setup scope. A same-scope entry that names a
// step in the group of the step that lists it is a sibling member.
func (f *File) validateNeeds(scope Scope, entries, setup scopeEntries) error {
	names := make([]string, 0, len(entries.Steps))
	for name := range entries.Steps {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		for _, dep := range entries.Steps[name].Needs {
			if err := validateNeedEntry(scope, name, dep, entries, setup); err != nil {
				return f.wrapAt(entries.Paths[name].Append(cue.Str("needs")), err)
			}
		}
	}
	return nil
}

// ResolveNeed resolves one of name's own same-scope needs entries to its DAG
// node name. A group member may name a sibling by its bare name, which
// resolves to "<group>.<sibling>"; a plain step or a group's own name is its
// own node name. ok is false for a "setup." entry, and for a name that is
// neither a step nor a group.
func ResolveNeed(name string, dep Need, steps map[string]Step, groups map[string]Group) (string, bool) {
	if dep.Setup {
		return "", false
	}
	if group, _, ok := strings.Cut(name, GroupMemberSep); ok && slices.Contains(groups[group].Members, dep.Step) {
		return MemberName(group, dep.Step), true
	}
	if _, ok := steps[dep.Step]; ok {
		return dep.Step, true
	}
	if _, ok := groups[dep.Step]; ok {
		return dep.Step, true
	}
	return "", false
}

// validateNeedEntry checks one needs entry of the step name in scope.
func validateNeedEntry(scope Scope, name string, dep Need, entries, setup scopeEntries) error {
	if dep.Setup {
		if scope != ScopeEnv {
			return fmt.Errorf("config: %s.%s: needs %q: only an env step can use a %q dependency: %w",
				scope, name, dep.String(), setupNeedPrefix, ErrCrossScopeNeed)
		}
		if _, ok := setup.Steps[dep.Step]; ok {
			return nil
		}
		if _, ok := setup.Groups[dep.Step]; ok {
			return nil
		}
		return fmt.Errorf("config: %s.%s: needs %q: no such step in scope %q: %w",
			scope, name, dep.String(), ScopeSetup, ErrUnknownStep)
	}
	if _, ok := ResolveNeed(name, dep, entries.Steps, entries.Groups); ok {
		return nil
	}
	return fmt.Errorf("config: %s.%s: needs %q: no such step in scope %q: %w",
		scope, name, dep.String(), scope, ErrUnknownStep)
}
