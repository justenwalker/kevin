package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestGroups(t *testing.T) {
	t.Run("flattens members and unions the group's needs into each", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	net: uses: "echo:echo"
	db: {
		needs: ["net"]
		steps: {
			primary: uses: "echo:echo"
			replica: {uses: "echo:echo", needs: ["primary"]}
		}
	}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
		cfg, err := f.Config()
		require.NoError(t, err)

		env := cfg.Steps(config.ScopeEnv)
		require.Len(t, env, 3)
		assert.ElementsMatch(t, []string{"net"}, env["db.primary"].Needs)
		assert.ElementsMatch(t, []string{"primary", "net"}, env["db.replica"].Needs)

		groups := cfg.Groups(config.ScopeEnv)
		require.Contains(t, groups, "db")
		assert.Equal(t, []string{"primary", "replica"}, groups["db"].Members)
		assert.Equal(t, []string{"net"}, groups["db"].Needs)
	})

	t.Run("a member that already declares its group's need is not given it twice", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	net: uses: "echo:echo"
	db: {
		needs: ["net"]
		steps: primary: {uses: "echo:echo", needs: ["net"]}
	}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
		cfg, err := f.Config()
		require.NoError(t, err)

		assert.Equal(t, []string{"net"}, cfg.Steps(config.ScopeEnv)["db.primary"].Needs)
	})

	t.Run("validates a member's with block against its effective needs", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	net: uses: "echo:echo"
	db: {
		needs: ["net"]
		steps: primary: {uses: "echo:echo", with: msg: "${needs.net.out.x}"}
	}
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
	})

	t.Run("outputs computed from members, needs the same needs.<member> convention", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: {
	steps: primary: uses: "echo:echo"
	outputs: addr: "${needs.primary.out.address}"
}
`)
		require.NoError(t, f.Validate(offers("echo", "echo")))
		cfg, err := f.Config()
		require.NoError(t, err)

		groups := cfg.Groups(config.ScopeEnv)
		require.Contains(t, groups, "db")
		assert.Equal(t, map[string]string{"addr": "${needs.primary.out.address}"}, groups["db"].Outputs)
	})

	t.Run("outputs referencing an undeclared member fails statically", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: {
	steps: primary: uses: "echo:echo"
	outputs: addr: "${needs.nope.out.address}"
}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUndeclaredNeed)
	})

	t.Run("a step outside the group cannot address a member directly", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: {
	db: steps: primary: uses: "echo:echo"
	web: {uses: "echo:echo", needs: ["db.primary"]}
}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUnaddressableMember)
	})

	t.Run("a group's own needs cannot address one of its own members", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: {
	needs: ["db.primary"]
	steps: primary: uses: "echo:echo"
}
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrUnaddressableMember)
	})

	t.Run("a command cannot address a member directly", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: steps: primary: uses: "echo:echo"
commands: shell: {needs: ["db.primary"], run: ["echo", "hi"]}
`)
		err := f.Validate(offersExport(true))
		require.ErrorIs(t, err, config.ErrUnaddressableMember)
	})

	t.Run("a dotted key is reserved", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: "db.primary": uses: "echo:echo"
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrReservedKeyChar)
	})

	t.Run("a dotted member key is reserved", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: steps: "a.b": uses: "echo:echo"
`)
		err := f.Validate(offers("echo", "echo"))
		require.ErrorIs(t, err, config.ErrReservedKeyChar)
	})

	t.Run("validates each member against its own step type's schema, not a sibling's", func(t *testing.T) {
		schemas := map[string]config.PluginSchemas{
			"echo": {Steps: map[string][]byte{
				"echo": []byte(`#Config: msg!: string`),
				"fail": []byte(`#Config: code!: int`),
			}},
		}
		f := load(t, `
plugins: echo: cmd: "echo"
env: g: steps: {
	a: {uses: "echo:echo", with: msg: "hi"}
	b: {uses: "echo:fail", needs: ["a"], with: code: 1}
}
`)
		require.NoError(t, f.Validate(schemas))
	})

	t.Run("StepPlugins enumerates a plugin used only inside a group", func(t *testing.T) {
		f := load(t, `
plugins: echo: cmd: "echo"
env: db: steps: primary: uses: "echo:echo"
`)
		names, err := f.StepPlugins()
		require.NoError(t, err)
		assert.Equal(t, []string{"echo"}, names)
	})
}
