package engine

import (
	"github.com/justenwalker/kevin/internal/plugins/launcher"
	"github.com/justenwalker/kevin/internal/step"
)

// stepKind returns the Kind that info's step type name reports, or
// step.KindUnspecified when info offers no such step.
func stepKind(info launcher.Info, name string) step.Kind {
	st, _ := info.Step(name)
	return st.Kind
}

// stepIdempotent reports whether info's step type name is safe to call Up on
// again.
func stepIdempotent(info launcher.Info, name string) bool {
	st, _ := info.Step(name)
	return st.Idempotent
}

// stepImplementsDown reports whether info's step type name implements Down.
func stepImplementsDown(info launcher.Info, name string) bool {
	st, _ := info.Step(name)
	return st.Down
}

// isCompactStep reports whether the console should render this step as a
// single muted line instead of a full card: a probe by definition, plus
// builtin:route, which only registers a proxy mapping and creates nothing
// of its own despite being kind action. Both are gates for some other
// resource/action, not something worth equal visual weight.
func isCompactStep(kind, plugin, step string) bool {
	return kind == "probe" || (plugin == "builtin" && step == "route")
}
