// Command kevin-plugin-echo is a plugin that creates no resource. The plugin
// exercises the whole plugin protocol: the identity, the schema validation,
// the log and progress streams, the published outputs, and the removal.
package main

import "github.com/justenwalker/kevin/plugin"

func main() {
	plugin.Serve(plugin.Plugin{
		Name:         "echo",
		Version:      version,
		ConfigSchema: configSchema,
		Steps: map[string]plugin.Step{
			"echo":  echo{},
			"fail":  failStep{},
			"probe": probeStep{},
		},
		Icon:      icon,
		Configure: configure,
	})
}
