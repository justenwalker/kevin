package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"
)

// GroupMemberSep joins a group's own name to a member's bare name for the
// member's engine-internal DAG node name ("<group><sep><member>"). No user
// key or needs entry may contain it outside that join: it is the only
// source of a dotted step name.
const GroupMemberSep = "."

// MemberName builds a group member's DAG node name.
func MemberName(group, member string) string {
	return group + GroupMemberSep + member
}

// clipPath returns p with its selector slice's capacity trimmed to its
// length, so a later Append on the result always allocates its own backing
// array instead of aliasing another Append's result derived from the same
// p.
func clipPath(p cue.Path) cue.Path {
	return cue.MakePath(slices.Clip(p.Selectors())...)
}

// Group is a step group: a container with no behavior of its own. Its
// member steps are flattened into the scope's Setup/Env map (see
// [Config.Steps]) at "<group>.<member>" by [File.decodeScope], each
// member's Needs already unioned with Needs below - Group itself carries
// what a member doesn't: the needs every member implicitly shares, the
// member list, the group's own computed outputs, and its label.
type Group struct {
	Needs   []Need
	Members []string // bare member names, sorted
	Outputs map[string]string
	Label   string
}

// rawGroupEntry is #StepGroup's JSON shape - decoded only once
// [isGroupEntry] has identified a scope entry as a group, not a step.
type rawGroupEntry struct {
	Steps   map[string]json.RawMessage `json:"steps"`
	Needs   []Need                     `json:"needs"`
	Outputs map[string]string          `json:"outputs"`
	Label   string                     `json:"label"`
}

// isGroupEntry reports whether raw is a #StepGroup (it carries a "steps"
// key) rather than a #Step.
func isGroupEntry(raw json.RawMessage) (bool, error) {
	var peek struct {
		Steps json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return false, fmt.Errorf("config: decode: %w", err)
	}
	return peek.Steps != nil, nil
}

// validateNeedsSyntax rejects a needs entry that contains "." other than
// the reserved "setup.<name>" cross-scope prefix. No legal needs entry
// otherwise contains a dot, so this is the single check that keeps a group
// member's internal "<group>.<member>" name unaddressable from outside its
// own group: a step, a group, or a command can only ever spell out a bare
// name (or "setup.<bare name>"), never a flattened member name.
func validateNeedsSyntax(needs []Need) error {
	for _, n := range needs {
		if strings.Contains(n.Step, GroupMemberSep) {
			return fmt.Errorf("%w: %q", ErrUnaddressableMember, n.String())
		}
	}
	return nil
}

// scopeEntries is [File.decodeScope]'s flattened, validated view of one
// scope's block: every #Step (a plain step, or a group's member flattened
// at "<group>.<member>") with its resolvable CUE source Path, plus each
// #StepGroup's own metadata by its bare name.
type scopeEntries struct {
	Steps  map[string]Step
	Paths  map[string]cue.Path
	Groups map[string]Group
}

// decodeScope reads scopeName's block, discriminating each entry as a
// #Step or #StepGroup by [isGroupEntry], then flattens a group's members
// into Steps (unioning the group's own needs into each member's needs) and
// statically validates the group's own outputs block the same way an
// ordinary step's with block gets validated - all before anything runs,
// per ADR-0002.
func (f *File) decodeScope(scopeName Scope) (scopeEntries, error) {
	// A whole-document decode, like every other File method uses (not
	// decodePath scoped to just this field) - so an incomplete value
	// elsewhere in the file (e.g. an unset plugins.<name>.cmd) still
	// surfaces here as ErrInvalid, consistent with the rest of the package.
	var out struct {
		Setup map[string]json.RawMessage `json:"setup"`
		Env   map[string]json.RawMessage `json:"env"`
	}
	if err := f.decode(&out); err != nil {
		return scopeEntries{}, err
	}
	raw := out.Env
	if scopeName == ScopeSetup {
		raw = out.Setup
	}

	scopePath := clipPath(cue.MakePath(cue.Str(scopeName.String())))
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	entries := scopeEntries{
		Steps:  make(map[string]Step, len(raw)),
		Paths:  make(map[string]cue.Path, len(raw)),
		Groups: make(map[string]Group),
	}

	for _, name := range names {
		entryPath := clipPath(scopePath.Append(cue.Str(name)))
		if strings.Contains(name, GroupMemberSep) {
			return scopeEntries{}, f.reservedKeyErr(entryPath)
		}

		isGroup, err := isGroupEntry(raw[name])
		if err != nil {
			return scopeEntries{}, err
		}
		if !isGroup {
			step, err := f.decodeStep(raw[name], entryPath, fmt.Sprintf("%s.%s", scopeName, name))
			if err != nil {
				return scopeEntries{}, err
			}
			if err := validateNeedsSyntax(step.Needs); err != nil {
				return scopeEntries{}, f.wrapAt(entryPath.Append(cue.Str("needs")),
					fmt.Errorf("config: %s.%s.needs: %w", scopeName, name, err))
			}
			entries.Steps[name] = step
			entries.Paths[name] = entryPath
			continue
		}

		if err := f.decodeGroup(scopeName, name, entryPath, raw[name], &entries); err != nil {
			return scopeEntries{}, err
		}
	}

	return entries, nil
}

// decodeStep decodes raw into a Step. A bad step type reference or timeout
// is reported at its own field of the step at path, as [File.Validate] reports
// every other field. label names the step in the message.
func (f *File) decodeStep(raw json.RawMessage, path cue.Path, label string) (Step, error) {
	var step Step
	err := json.Unmarshal(raw, &step)
	if err == nil {
		return step, nil
	}
	err = fmt.Errorf("config: %s: %w", label, err)
	switch {
	case errors.Is(err, ErrBadTimeout):
		return Step{}, f.wrapAt(path.Append(cue.Str("timeout")), err)
	case errors.Is(err, ErrBadStepRef):
		return Step{}, f.wrapAt(path.Append(cue.Str("uses")), err)
	default:
		return Step{}, err
	}
}

// decodeGroup decodes name's #StepGroup entry (already identified by
// isGroupEntry) into entries: flattens its members into entries.Steps at
// "<group>.<member>" (each member's needs unioned with the group's own),
// statically validates its outputs block, and records its own Group
// metadata.
func (f *File) decodeGroup(scopeName Scope, name string, groupPath cue.Path, raw json.RawMessage, entries *scopeEntries) error {
	var g rawGroupEntry
	if err := json.Unmarshal(raw, &g); err != nil {
		return fmt.Errorf("config: %s.%s: %w", scopeName, name, err)
	}
	if err := validateNeedsSyntax(g.Needs); err != nil {
		return f.wrapAt(groupPath.Append(cue.Str("needs")), fmt.Errorf("config: %s.%s.needs: %w", scopeName, name, err))
	}

	members := make([]string, 0, len(g.Steps))
	for member := range g.Steps {
		members = append(members, member)
	}
	sort.Strings(members)

	stepsPath := clipPath(groupPath.Append(cue.Str("steps")))
	for _, member := range members {
		memberPath := clipPath(stepsPath.Append(cue.Str(member)))
		if strings.Contains(member, GroupMemberSep) {
			return f.reservedKeyErr(memberPath)
		}
		step, err := f.decodeStep(g.Steps[member], memberPath, fmt.Sprintf("%s.%s.steps.%s", scopeName, name, member))
		if err != nil {
			return err
		}
		if err := validateNeedsSyntax(step.Needs); err != nil {
			return f.wrapAt(memberPath.Append(cue.Str("needs")),
				fmt.Errorf("config: %s.%s.steps.%s.needs: %w", scopeName, name, member, err))
		}
		step.Needs = unionNeeds(step.Needs, g.Needs)

		flat := MemberName(name, member)
		entries.Steps[flat] = step
		entries.Paths[flat] = memberPath
	}

	if len(g.Outputs) > 0 {
		outputsJSON, err := json.Marshal(g.Outputs)
		if err != nil {
			return fmt.Errorf("config: %s.%s.outputs: %w", scopeName, name, err)
		}
		if refErr := validateNeedsReferences(scopeName.String(), name, "outputs", memberNeeds(members), outputsJSON); refErr != nil {
			return f.wrapAt(groupPath.Append(cue.Str("outputs")), refErr)
		}
	}

	entries.Groups[name] = Group{
		Needs:   g.Needs,
		Members: members,
		Outputs: g.Outputs,
		Label:   g.Label,
	}
	return nil
}

// memberNeeds lists members as same-scope needs, for checking a group's
// outputs block against them.
func memberNeeds(members []string) []Need {
	out := make([]Need, len(members))
	for i, m := range members {
		out[i] = Need{Step: m}
	}
	return out
}

// unionNeeds returns a's entries followed by any of b's entries not
// already in a, preserving a's order - a member's own needs first, then
// whatever its group implicitly adds.
func unionNeeds(a, b []Need) []Need {
	if len(b) == 0 {
		return a
	}
	seen := make(map[Need]struct{}, len(a)+len(b))
	for _, n := range a {
		seen[n] = struct{}{}
	}
	out := a
	for _, n := range b {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// reservedKeyErr reports [ErrReservedKeyChar] for the key at p.
func (f *File) reservedKeyErr(p cue.Path) error {
	return f.wrapAt(p, fmt.Errorf("config: %s: %w", p, ErrReservedKeyChar))
}

// wrapAt wraps err with the source position of p, as [File.invalid]'s
// ValidationError.
func (f *File) wrapAt(p cue.Path, err error) error {
	return f.invalid(cueerrors.Wrapf(err, f.value.LookupPath(p).Pos(), ""))
}
