// Package output defines the value type a step publishes to its dependents.
package output

import "github.com/justenwalker/kevin/protos/pb"

// Value is one output value a step produces.
type Value struct {
	// String is the value's content.
	String string

	// Sensitive is true when String must never be logged or displayed in
	// full - a generated password or token, for example.
	Sensitive bool
}

// Map is a step's outputs, keyed by output name. Its dependents receive it
// when the step is walked.
type Map map[string]Value

// FromProto converts wire outputs. A nil message or one with no values is a
// nil Map.
func FromProto(o *pb.Outputs) Map {
	values := o.GetValues()
	if len(values) == 0 {
		return nil
	}
	out := make(Map, len(values))
	for k, v := range values {
		out[k] = Value{String: v.GetStringValue(), Sensitive: v.GetSensitive()}
	}
	return out
}

// ToProto converts outputs to the wire. An empty Map is nil.
func (m Map) ToProto() *pb.Outputs {
	if len(m) == 0 {
		return nil
	}
	out := &pb.Outputs{Values: make(map[string]*pb.Value, len(m))}
	for k, v := range m {
		out.Values[k] = &pb.Value{Kind: &pb.Value_StringValue{StringValue: v.String}, Sensitive: v.Sensitive}
	}
	return out
}
