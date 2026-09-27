package config

import (
	"encoding/json"
	"fmt"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/format"
)

// pathVariables is the top-level "variables" field's own path.
var pathVariables = cue.MakePath(cue.Str("variables"))

// fieldType is #Variable's "type" field name (see schema.cue) - the one
// field the core schema deliberately allows to stay a non-concrete
// constraint (e.g. "int & >=1 & <=10") rather than a concrete value.
const fieldType = "type"

// marshalJSON is f.value's JSON encoding, skipping each declared variable's
// "type" field. [cue.Value.MarshalJSON] fails outright on the first
// non-concrete leaf found anywhere in the value it's called on, and a
// declared variable's "type" is the only field the core schema allows to
// stay non-concrete - so every [File.decode] call must go through this,
// never [cue.Value.MarshalJSON] on f.value directly.
func (f *File) marshalJSON() ([]byte, error) {
	varsVal := f.value.LookupPath(pathVariables)
	if !varsVal.Exists() {
		return f.value.MarshalJSON() //nolint:wrapcheck // caller (File.decode) wraps this
	}
	hasType, err := anyVariableHasType(varsVal)
	if err != nil {
		return nil, err
	}
	if !hasType {
		return f.value.MarshalJSON() //nolint:wrapcheck // caller (File.decode) wraps this
	}
	return marshalSkippingVariableTypes(f.value)
}

// validateConcrete checks f.value is fully concrete, except each declared
// variable's own "type" field (see marshalJSON).
func (f *File) validateConcrete() error {
	varsVal := f.value.LookupPath(pathVariables)
	if !varsVal.Exists() {
		return f.value.Validate(cue.Concrete(true)) //nolint:wrapcheck // caller wraps this
	}
	hasType, err := anyVariableHasType(varsVal)
	if err != nil {
		return err
	}
	if !hasType {
		return f.value.Validate(cue.Concrete(true)) //nolint:wrapcheck // caller wraps this
	}
	return validateConcreteSkippingVariableTypes(f.value)
}

// anyVariableHasType reports whether any field of varsVal (the "variables"
// struct) declares a "type".
func anyVariableHasType(varsVal cue.Value) (bool, error) {
	iter, err := varsVal.Fields()
	if err != nil {
		return false, fmt.Errorf("config: variables: %w", err)
	}
	for iter.Next() {
		if iter.Value().LookupPath(cue.MakePath(cue.Str(fieldType))).Exists() {
			return true, nil
		}
	}
	return false, nil
}

// marshalSkippingVariableTypes marshals val to JSON field by field at the
// top level, substituting variables' own field-by-field marshal (which
// itself skips "type") for the plain "variables" field's marshal.
func marshalSkippingVariableTypes(val cue.Value) ([]byte, error) {
	out := make(map[string]json.RawMessage)
	iter, err := val.Fields()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		if name == "variables" {
			varsData, varsErr := marshalVariablesSkippingType(iter.Value())
			if varsErr != nil {
				return nil, varsErr
			}
			out[name] = varsData
			continue
		}
		fieldData, fieldErr := iter.Value().MarshalJSON()
		if fieldErr != nil {
			return nil, fmt.Errorf("config: %s: %w", name, fieldErr)
		}
		out[name] = fieldData
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return data, nil
}

// marshalVariablesSkippingType marshals varsVal (the "variables" struct) to
// JSON, omitting each declared variable's own "type" field.
func marshalVariablesSkippingType(varsVal cue.Value) ([]byte, error) {
	out := make(map[string]json.RawMessage)
	iter, err := varsVal.Fields()
	if err != nil {
		return nil, fmt.Errorf("config: variables: %w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		declData, declErr := marshalVariableSkippingType(iter.Value())
		if declErr != nil {
			return nil, fmt.Errorf("config: variables.%s: %w", name, declErr)
		}
		out[name] = declData
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("config: variables: %w", err)
	}
	return data, nil
}

// marshalVariableSkippingType marshals declVal (one "variables.<name>"
// struct) to JSON, omitting its "type" field.
func marshalVariableSkippingType(declVal cue.Value) ([]byte, error) {
	out := make(map[string]json.RawMessage)
	iter, err := declVal.Fields()
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		if name == fieldType {
			continue
		}
		fieldData, fieldErr := iter.Value().MarshalJSON()
		if fieldErr != nil {
			return nil, fmt.Errorf("%s: %w", name, fieldErr)
		}
		out[name] = fieldData
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("%w", err)
	}
	return data, nil
}

// validateConcreteSkippingVariableTypes checks val is fully concrete, field
// by field at the top level, substituting a per-variable, type-skipping
// concreteness check for the plain "variables" field's check.
func validateConcreteSkippingVariableTypes(val cue.Value) error {
	iter, err := val.Fields()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		if name == "variables" {
			if err := validateVariablesConcreteSkippingType(iter.Value()); err != nil {
				return err
			}
			continue
		}
		if err := iter.Value().Validate(cue.Concrete(true)); err != nil {
			return fmt.Errorf("config: %s: %w", name, err)
		}
	}
	return nil
}

// validateVariablesConcreteSkippingType checks varsVal (the "variables"
// struct) is fully concrete, except each declared variable's "type" field.
func validateVariablesConcreteSkippingType(varsVal cue.Value) error {
	iter, err := varsVal.Fields()
	if err != nil {
		return fmt.Errorf("config: variables: %w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		declIter, err := iter.Value().Fields()
		if err != nil {
			return fmt.Errorf("config: variables.%s: %w", name, err)
		}
		for declIter.Next() {
			fieldName := declIter.Selector().Unquoted()
			if fieldName == fieldType {
				continue
			}
			if err := declIter.Value().Validate(cue.Concrete(true)); err != nil {
				return fmt.Errorf("config: variables.%s.%s: %w", name, fieldName, err)
			}
		}
	}
	return nil
}

// fillVariableTypes populates vars[name].Type from f.value's
// "variables.<name>.type" field, for every declared variable that sets one
// - the raw CUE source of that constraint, since it's often non-concrete
// and can't be decoded through [File.decode]'s JSON path (see
// [Variable.Type]). A declared variable missing from vars is skipped: a
// caller may have decoded only a subset (e.g. [File.Validate]'s inline
// decode uses the same map shape [File.Config] does).
func (f *File) fillVariableTypes(vars map[string]Variable) error {
	varsVal := f.value.LookupPath(pathVariables)
	if !varsVal.Exists() {
		return nil
	}
	iter, err := varsVal.Fields()
	if err != nil {
		return fmt.Errorf("config: variables: %w", err)
	}
	for iter.Next() {
		name := iter.Selector().Unquoted()
		decl, ok := vars[name]
		if !ok {
			continue
		}
		typeVal := iter.Value().LookupPath(cue.MakePath(cue.Str(fieldType)))
		if !typeVal.Exists() {
			continue
		}
		src, err := format.Node(typeVal.Syntax(cue.Final()))
		if err != nil {
			return fmt.Errorf("config: variables.%s.type: %w", name, err)
		}
		decl.Type = src
		vars[name] = decl
	}
	return nil
}
