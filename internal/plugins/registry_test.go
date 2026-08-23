package plugins_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/plugins"
)

func TestLookup(t *testing.T) {
	tests := []struct {
		name string
		find string
		want bool
	}{
		{name: "container is registered", find: "container", want: true},
		{name: "kind is registered", find: "kind", want: true},
		{name: "trust is registered", find: "trust", want: true},
		{name: "kubectl is registered", find: "kubectl", want: true},
		{name: "helm is registered", find: "helm", want: true},
		{name: "wait is registered", find: "wait", want: true},
		{name: "route is registered", find: "route", want: true},
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
	assert.Equal(t, []string{"container", "helm", "kind", "kubectl", "route", "trust", "wait"}, plugins.Names(),
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
