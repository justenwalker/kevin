package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/internal/step"
)

// commandRun loads src as an environment whose only plugin is a mock, and
// returns a run wired to it the way PlanCommand wires a real one.
func commandRun(t *testing.T, src string, plugin stepPlugin) *run {
	t.Helper()
	f, err := config.Load(configDir(t, proxyBlock(t)+"plugins: echo: cmd: \"echo\"\n"+src), "", nil)
	require.NoError(t, err)
	schemas := map[string]config.PluginSchemas{"echo": {
		Steps:  map[string][]byte{"echo": nil, "noexport": nil},
		Export: map[string]bool{"echo": true},
	}}
	require.NoError(t, f.Validate(schemas))
	cfg, err := f.Config()
	require.NoError(t, err)

	return &run{
		cfg:     cfg,
		scope:   config.ScopeEnv,
		steps:   cfg.Steps(config.ScopeEnv),
		groups:  cfg.Groups(config.ScopeEnv),
		plugins: map[string]stepPlugin{"echo": plugin},
		caps: map[string]pluginhost.Info{"echo": {Steps: []pluginhost.StepInfo{
			{Name: "echo", Export: true}, {Name: "noexport"},
		}}},
		env:     step.Env{},
		project: map[string]string{},
	}
}

func exportOf(values map[string]string) step.ExportResult {
	out := output.Map{}
	for k, v := range values {
		out[k] = output.Value{String: v}
	}
	return step.ExportResult{Out: out}
}

func TestPlanCommand(t *testing.T) {
	t.Run("renders run against an env step and a setup step", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		plugin.EXPECT().Export(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, req step.ExportRequest) (step.ExportResult, error) {
				return exportOf(map[string]string{"addr": req.Step + ":5432"}), nil
			})
		r := commandRun(t, `
setup: db: uses: "echo:echo"
env: web: uses: "echo:echo"
commands: psql: {
	needs: ["web", "setup.db"]
	run: ["psql", "${needs.web.out.addr}", "${setup.db.out.addr}"]
}
`, plugin)

		plan, err := r.planCommand(t.Context(), "psql")

		require.NoError(t, err)
		assert.Equal(t, []string{"psql", "web:5432", "db:5432"}, plan.Argv)
		assert.Equal(t, r.cfg.Dir, plan.Dir)
	})

	t.Run("resolves a setup group an env step needs", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		plugin.EXPECT().Export(mock.Anything, mock.Anything).RunAndReturn(
			func(_ context.Context, req step.ExportRequest) (step.ExportResult, error) {
				return exportOf(map[string]string{"host": req.Step}), nil
			})
		r := commandRun(t, `
setup: g: {
	steps: m: uses: "echo:echo"
	outputs: host: "${needs.m.out.host}"
}
env: web: {uses: "echo:echo", needs: ["setup.g"], with: {}}
commands: show: {
	needs: ["web"]
	run: ["echo", "${needs.web.out.host}"]
}
`, plugin)

		plan, err := r.planCommand(t.Context(), "show")

		require.NoError(t, err)
		assert.Equal(t, []string{"echo", "web"}, plan.Argv)
	})

	t.Run("lists the available commands for an unknown name", func(t *testing.T) {
		r := commandRun(t, `
env: web: uses: "echo:echo"
commands: a: run: ["a"]
commands: b: run: ["b"]
`, newMockstepPlugin(t))

		_, err := r.planCommand(t.Context(), "nope")

		require.ErrorContains(t, err, `no command named "nope", available: a, b`)
	})

	t.Run("reports a failed export with the need", func(t *testing.T) {
		plugin := newMockstepPlugin(t)
		plugin.EXPECT().Export(mock.Anything, mock.Anything).Return(step.ExportResult{}, assert.AnError)
		r := commandRun(t, `
env: web: uses: "echo:echo"
commands: c: {needs: ["web"], run: ["c"]}
`, plugin)

		_, err := r.planCommand(t.Context(), "c")

		require.ErrorIs(t, err, assert.AnError)
		require.ErrorContains(t, err, `needs "web"`)
	})

	t.Run("rejects a plugin that is not loaded", func(t *testing.T) {
		r := commandRun(t, `
env: web: uses: "echo:echo"
commands: c: {needs: ["web"], run: ["c"]}
`, newMockstepPlugin(t))
		r.plugins = nil

		_, err := r.planCommand(t.Context(), "c")

		require.ErrorContains(t, err, `plugin "echo" not loaded`)
	})
}

func TestCommandDir(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "an empty cwd is the project directory", cwd: "", want: "/proj"},
		{name: "a relative cwd is under the project directory", cwd: "sub", want: "/proj/sub"},
		{name: "an absolute cwd is used as is", cwd: "/elsewhere", want: "/elsewhere"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, commandDir("/proj", tt.cwd))
		})
	}
}
