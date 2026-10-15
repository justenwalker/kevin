package engine

import (
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

// stepKind returns the Kind that info's step type name reports, or
// STEP_KIND_UNSPECIFIED when info offers no such step.
func stepKind(info pluginhost.Info, name string) pb.StepKind {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Kind
		}
	}
	return pb.StepKind_STEP_KIND_UNSPECIFIED
}

// stepIdempotent reports whether info's step type name is safe to call Up on
// again.
func stepIdempotent(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Idempotent
		}
	}
	return false
}

// stepImplementsDown reports whether info's step type name implements Down.
func stepImplementsDown(info pluginhost.Info, name string) bool {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Down
		}
	}
	return false
}

// stepKindLabel returns the console-facing label for k, or "" for an
// unspecified kind.
func stepKindLabel(k pb.StepKind) string {
	switch k {
	case pb.StepKind_STEP_KIND_UNSPECIFIED:
		return ""
	case pb.StepKind_STEP_KIND_RESOURCE:
		return "resource"
	case pb.StepKind_STEP_KIND_ACTION:
		return "action"
	case pb.StepKind_STEP_KIND_PROBE:
		return "probe"
	default:
		return ""
	}
}

// isCompactStep reports whether the console should render this step as a
// single muted line instead of a full card: a probe by definition, plus
// builtin:route, which only registers a proxy mapping and creates nothing
// of its own despite being kind action. Both are gates for some other
// resource/action, not something worth equal visual weight.
func isCompactStep(kind, plugin, step string) bool {
	return kind == "probe" || (plugin == "builtin" && step == "route")
}
