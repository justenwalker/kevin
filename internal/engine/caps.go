package engine

import (
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/step"
)

// stepKind returns the Kind that info's step type name reports, or
// step.KindUnspecified when info offers no such step.
func stepKind(info pluginhost.Info, name string) step.Kind {
	for _, st := range info.Steps {
		if st.Name == name {
			return st.Kind
		}
	}
	return step.KindUnspecified
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

// isCompactStep reports whether the console should render this step as a
// single muted line instead of a full card: a probe by definition, plus
// builtin:route, which only registers a proxy mapping and creates nothing
// of its own despite being kind action. Both are gates for some other
// resource/action, not something worth equal visual weight.
func isCompactStep(kind, plugin, step string) bool {
	return kind == "probe" || (plugin == "builtin" && step == "route")
}
