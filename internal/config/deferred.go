package config

import (
	"bytes"
	"encoding/json"
	"fmt"

	"cuelang.org/go/cue"

	"github.com/justenwalker/kevin/internal/expr"
)

// unifyDeferred unifies schema with with, deferring a concreteness check on
// any field whose value is exactly one bare "${...}" marker (see
// [expr.BareMarker]) - a value only known once a step renders its with
// block at Up time, or a plugin's config block resolves against
// vars/env/project. Unifying schema directly against such a field's literal
// marker string conflicts outright when the schema types that field as
// anything but a string (the conflict happens at Unify, before any
// concreteness check runs) - so a bare-marker leaf is rebuilt as CUE's own
// top ("_") instead of its literal string, which unifies against any
// schema type with no conflict, and its path is reported in except for the
// caller to skip in its own concreteness check (see [concreteExcept]).
//
// raw is with's own JSON source (already decoded once by [File.decode]),
// used only to cheaply detect whether with carries any marker at all - the
// deferral machinery below is skipped entirely otherwise, so a with block
// with no "${...}" anywhere unifies exactly as before.
func unifyDeferred(ctx *cue.Context, schema, with cue.Value, raw json.RawMessage) (cue.Value, map[string]bool, error) {
	if !bytes.Contains(raw, []byte("${")) {
		return schema.Unify(with), nil, nil
	}

	skeleton := ctx.CompileString("_")
	except := make(map[string]bool)
	skeleton, err := deferMarkers(skeleton, cue.Path{}, with, except)
	if err != nil {
		return cue.Value{}, nil, fmt.Errorf("config: %w", err)
	}
	return schema.Unify(skeleton), except, nil
}

// deferMarkers walks with, filling root at path with each leaf's own value,
// except a bare-marker string leaf - left unfilled (CUE top, from root's
// own "_" starting point) and recorded in except instead.
func deferMarkers(root cue.Value, path cue.Path, with cue.Value, except map[string]bool) (cue.Value, error) {
	switch with.IncompleteKind() {
	case cue.StructKind:
		iter, err := with.Fields()
		if err != nil {
			return cue.Value{}, fmt.Errorf("%w", err)
		}
		for iter.Next() {
			var err error
			root, err = deferMarkers(root, path.Append(iter.Selector()), iter.Value(), except)
			if err != nil {
				return cue.Value{}, err
			}
		}
		return root, nil
	case cue.ListKind:
		iter, err := with.List()
		if err != nil {
			return cue.Value{}, fmt.Errorf("%w", err)
		}
		for i := 0; iter.Next(); i++ {
			var err error
			root, err = deferMarkers(root, path.Append(cue.Index(i)), iter.Value(), except)
			if err != nil {
				return cue.Value{}, err
			}
		}
		return root, nil
	case cue.StringKind:
		s, err := with.String()
		if err != nil {
			return cue.Value{}, fmt.Errorf("%w", err)
		}
		if _, ok := expr.BareMarker(s); ok {
			except[path.String()] = true
			return root, nil
		}
		return root.FillPath(path, with), nil
	default:
		return root.FillPath(path, with), nil
	}
}

// concreteExcept checks that v is fully concrete, except at any path
// except marks - a field [unifyDeferred] deferred, whose real type can only
// be checked once it's rendered (see the caller for when that happens).
func concreteExcept(v cue.Value, path cue.Path, except map[string]bool) error {
	if except[path.String()] {
		return nil
	}
	switch v.IncompleteKind() {
	case cue.StructKind:
		iter, err := v.Fields()
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		for iter.Next() {
			if err := concreteExcept(iter.Value(), path.Append(iter.Selector()), except); err != nil {
				return err
			}
		}
		return nil
	case cue.ListKind:
		iter, err := v.List()
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		for i := 0; iter.Next(); i++ {
			if err := concreteExcept(iter.Value(), path.Append(cue.Index(i)), except); err != nil {
				return err
			}
		}
		return nil
	default:
		return v.Validate(cue.Concrete(true)) //nolint:wrapcheck // caller wraps this with position/context
	}
}
