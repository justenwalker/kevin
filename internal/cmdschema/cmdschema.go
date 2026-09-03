// Package cmdschema reduces a *cobra.Command tree to a plain Go structure
// that a text/template can range over, so gen-docs depends on this
// package's types instead of cobra's own API.
package cmdschema

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Command is one cobra command, reduced for a template.
type Command struct {
	Name    string // last path segment of Use, e.g. "run", "install"
	Use     string
	Short   string
	Long    string
	Example string
	Flags   []Flag

	// Commands holds direct subcommands keyed by Name, so a template can
	// look one up by name: {{.Commands.install}}.
	Commands map[string]Command
}

// Flag is one flag defined directly on a Command (not inherited from a
// parent command).
type Flag struct {
	Name      string
	Shorthand string
	Type      string // pflag value type, e.g. "string", "bool"
	Default   string
	Doc       string
	Required  bool
}

// FromCommand walks c and its subcommands into a Command tree.
func FromCommand(c *cobra.Command) Command {
	var flags []Flag
	c.NonInheritedFlags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		_, required := f.Annotations[cobra.BashCompOneRequiredFlag]
		flags = append(flags, Flag{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   f.DefValue,
			Doc:       f.Usage,
			Required:  required,
		})
	})

	var subs map[string]Command
	for _, sc := range c.Commands() {
		if sc.Hidden {
			continue
		}
		if subs == nil {
			subs = map[string]Command{}
		}
		subs[sc.Name()] = FromCommand(sc)
	}

	return Command{
		Name:     c.Name(),
		Use:      c.Use,
		Short:    c.Short,
		Long:     c.Long,
		Example:  c.Example,
		Flags:    flags,
		Commands: subs,
	}
}
