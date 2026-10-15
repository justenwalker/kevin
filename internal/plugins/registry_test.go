package plugins_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/cueschema"
	"github.com/justenwalker/kevin/internal/plugins"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name string
		find string
		want bool
	}{
		{name: "container is registered", find: "container", want: true},
		{name: "fault is registered", find: "fault", want: true},
		{name: "kubernetes is registered", find: "kubernetes", want: true},
		{name: "kubectl is registered", find: "kubectl", want: true},
		{name: "helm is registered", find: "helm", want: true},
		{name: "wait is registered", find: "wait", want: true},
		{name: "route is registered", find: "route", want: true},
		{name: "exec is registered", find: "exec", want: true},
		{name: "unknown name is a miss", find: "nonsense", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, ok := plugins.Lookup(tt.find)
			assert.Equal(t, tt.want, ok, "Lookup must report whether the name is a builtin step type")
			if tt.want {
				assert.NotNil(t, step, "a hit must return a usable step")
			}
		})
	}
}

func TestNames(t *testing.T) {
	assert.Equal(t, []string{"container", "exec", "fault", "helm", "kubectl", "kubernetes", "route", "wait"}, plugins.Names(),
		"Names must list every builtin step type, sorted")
}

func TestProvider(t *testing.T) {
	p := plugins.Provider()

	assert.Equal(t, plugins.Name, p.Name, "the provider must identify as builtin")
	assert.NotEmpty(t, p.Version)
	assert.Nil(t, p.Configure, "the builtin provider takes no configuration")

	require.NotNil(t, p.Steps)
	for _, name := range plugins.Names() {
		step, ok := p.Steps[name]
		assert.True(t, ok, "the provider must offer %q", name)
		assert.NotEmpty(t, step.Schema(), "%q must declare a schema", name)
	}
}

// TestSchemasHaveNoOptionalDefaults checks that no field of a definition is
// declared with "?" and a default: such a default is never sent to the
// plugin. Fields of nested inline structs are not checked.
func TestSchemasHaveNoOptionalDefaults(t *testing.T) {
	for _, name := range plugins.Names() {
		step, ok := plugins.Lookup(name)
		require.True(t, ok)

		schema, err := cueschema.ParseSource(name, step.Schema())
		require.NoError(t, err)
		for def, d := range schema.Definitions {
			for _, f := range d.Fields {
				assert.False(t, f.Optional && f.Default != "", "%s #%s.%s is optional with default %s", name, def, f.Name, f.Default)
			}
		}
	}
}
