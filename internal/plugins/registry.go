// Package plugins holds the builtin kevin plugins.
//
// A builtin plugin ships inside the kevin binary. A project reaches one
// through the "builtin:" namespace of a plugin reference.
package plugins

import (
	"slices"

	"github.com/justenwalker/kevin/internal/plugins/container"
	"github.com/justenwalker/kevin/internal/plugins/helm"
	"github.com/justenwalker/kevin/internal/plugins/kind"
	"github.com/justenwalker/kevin/internal/plugins/kubectl"
	"github.com/justenwalker/kevin/internal/plugins/route"
	"github.com/justenwalker/kevin/internal/plugins/wait"
	"github.com/justenwalker/kevin/plugin"
)

// Name identifies the builtin provider. A project reaches it through the
// "builtin:" namespace of a plugin reference.
const Name = "builtin"

// version holds the version string. The build sets this value.
var version = "dev"

// steps maps each step type that the builtin provider offers to its
// implementation.
var steps = map[string]plugin.Step{
	"container": container.New(),
	"kind":      kind.New(),
	"kubectl":   kubectl.New(),
	"helm":      helm.New(),
	"wait":      wait.New(),
	"route":     route.New(),
}

// Provider returns the plugin that kevin supplies. It offers container,
// kind, kubectl, helm, wait, and route.
func Provider() plugin.Plugin {
	return plugin.Plugin{Name: Name, Version: version, Steps: steps}
}

// Lookup returns the builtin step type of that name.
func Lookup(name string) (plugin.Step, bool) { //nolint:ireturn // a lookup by name has no single concrete type to return, that's the whole point
	step, ok := steps[name]
	return step, ok
}

// Names returns every step type that the builtin provider offers, sorted.
func Names() []string {
	names := make([]string, 0, len(steps))
	for name := range steps {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
