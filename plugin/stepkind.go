package plugin

import "github.com/justenwalker/kevin/protos/pb"

// StepKind classifies what a step type is, for documentation and the
// console. It is descriptive metadata and does not change
// engine behavior.
type StepKind int32

//go:generate go tool -modfile=../tools.mod stringer -type=StepKind -linecomment
const (
	// StepKindUnspecified is the zero value: no kind was reported.
	StepKindUnspecified StepKind = iota //

	// StepKindResource creates and destroys something.
	StepKindResource // resource

	// StepKindAction mutates state that another system owns. StepKindAction
	// has no lifecycle of its own. It only applies changes.
	StepKindAction // action

	// StepKindProbe creates nothing. StepKindProbe only checks that something
	// else is ready.
	StepKindProbe // probe
)

// stepKindToProto maps the plugin SDK's StepKind to the wire enum.
func stepKindToProto(k StepKind) pb.StepKind {
	switch k {
	case StepKindUnspecified:
		return pb.StepKind_STEP_KIND_UNSPECIFIED
	case StepKindResource:
		return pb.StepKind_STEP_KIND_RESOURCE
	case StepKindAction:
		return pb.StepKind_STEP_KIND_ACTION
	case StepKindProbe:
		return pb.StepKind_STEP_KIND_PROBE
	default:
		return pb.StepKind_STEP_KIND_UNSPECIFIED
	}
}
