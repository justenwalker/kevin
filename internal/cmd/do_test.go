package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestSplitDoArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		dash      int
		wantName  string
		wantExtra []string
		wantErr   bool
	}{
		{name: "name only", args: []string{"shell"}, dash: -1, wantName: "shell", wantExtra: nil},
		{
			name: "name and extra args", args: []string{"shell", "-c", "select 1"}, dash: 1,
			wantName: "shell", wantExtra: []string{"-c", "select 1"},
		},
		{name: "too many names, no dash", args: []string{"a", "b"}, dash: -1, wantErr: true},
		{name: "too many names, with dash", args: []string{"a", "b", "-c"}, dash: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, extra, err := splitDoArgs(tt.args, tt.dash)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantExtra, extra)
		})
	}
}

func TestChdirToCwd(t *testing.T) {
	project := t.TempDir()
	sub := filepath.Join(project, "sub")
	require.NoError(t, os.Mkdir(sub, 0o750))
	abs := t.TempDir()

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "an empty cwd is the project directory", cwd: "", want: project},
		{name: "a relative cwd is under the project directory", cwd: "sub", want: sub},
		{name: "an absolute cwd is used as is", cwd: abs, want: abs},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())

			require.NoError(t, chdirToCwd(project, tt.cwd))

			got, err := os.Getwd()
			require.NoError(t, err)
			assert.Equal(t, evalSymlinks(t, tt.want), evalSymlinks(t, got))
		})
	}

	t.Run("reports a directory that does not exist", func(t *testing.T) {
		t.Chdir(t.TempDir())

		require.ErrorContains(t, chdirToCwd(project, "missing"), "do:")
	})
}

func evalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

func TestResolveCommandOutputs(t *testing.T) {
	cfg := &config.Config{
		Env:   map[string]config.Step{"bad": {Uses: "container"}, "web": {Uses: "builtin:container"}},
		Setup: map[string]config.Step{"db": {Uses: "builtin:container"}},
	}
	tests := []struct {
		name  string
		needs []string
		want  string
	}{
		{name: "an env step that does not exist", needs: []string{"nope"}, want: `no such step in scope "env"`},
		{name: "a setup step that does not exist", needs: []string{"setup.nope"}, want: `no such step in scope "setup"`},
		{name: "a malformed step type", needs: []string{"bad"}, want: "not a step type"},
		{name: "a plugin that is not loaded", needs: []string{"web"}, want: `plugin "builtin" not loaded`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolveCommandOutputs(t.Context(), cfg, nil, nil, &pb.Environment{}, tt.needs)

			require.ErrorContains(t, err, tt.want)
		})
	}

	t.Run("no needs resolve to nothing", func(t *testing.T) {
		needs, setup, err := resolveCommandOutputs(t.Context(), cfg, nil, nil, &pb.Environment{}, nil)

		require.NoError(t, err)
		assert.Nil(t, needs)
		assert.Nil(t, setup)
	})
}
