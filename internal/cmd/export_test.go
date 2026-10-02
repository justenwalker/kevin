package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/pluginhost"
	"github.com/justenwalker/kevin/protos/pb"
)

func TestSetupCrossScopeDeps(t *testing.T) {
	info := map[string]pluginhost.Info{
		"builtin": {Steps: []pluginhost.StepInfo{{Name: "container", Export: true}, {Name: "wait"}}},
	}
	tests := []struct {
		name    string
		cfg     config.Config
		needs   []string
		plugins map[string]*pluginhost.Client
		want    string
	}{
		{
			name:  "ignores a need that is not a setup step",
			cfg:   config.Config{},
			needs: []string{"db"},
		},
		{
			name:  "names a setup step that does not exist",
			cfg:   config.Config{},
			needs: []string{"setup.db"},
			want:  `no such step in scope "setup"`,
		},
		{
			name:  "rejects a malformed step type",
			cfg:   config.Config{Setup: map[string]config.Step{"db": {Uses: "container"}}},
			needs: []string{"setup.db"},
			want:  "not a step type",
		},
		{
			name:  "names a plugin that is not loaded",
			cfg:   config.Config{Setup: map[string]config.Step{"db": {Uses: "builtin:container"}}},
			needs: []string{"setup.db"},
			want:  `plugin "builtin" not loaded`,
		},
		{
			name:    "rejects a step type without export",
			cfg:     config.Config{Setup: map[string]config.Step{"db": {Uses: "builtin:wait"}}},
			needs:   []string{"setup.db"},
			plugins: map[string]*pluginhost.Client{"builtin": nil},
			want:    "does not implement export",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := setupCrossScopeDeps(t.Context(), &tt.cfg, tt.plugins, info, &pb.Environment{}, config.Step{Needs: tt.needs})
			if tt.want == "" {
				require.NoError(t, err)
				assert.Nil(t, out)
				return
			}
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestOutputsFromProto(t *testing.T) {
	t.Run("is nil without values", func(t *testing.T) {
		assert.Nil(t, outputsFromProto(nil))
		assert.Nil(t, outputsFromProto(&pb.Outputs{}))
	})

	t.Run("keeps the value and its sensitivity", func(t *testing.T) {
		got := outputsFromProto(&pb.Outputs{Values: map[string]*pb.Value{
			"url":   {Kind: &pb.Value_StringValue{StringValue: "http://x"}},
			"token": {Kind: &pb.Value_StringValue{StringValue: "s3"}, Sensitive: true},
		}})

		assert.Equal(t, output.Value{String: "http://x"}, got["url"])
		assert.Equal(t, output.Value{String: "s3", Sensitive: true}, got["token"])
	})
}

func TestStepExports(t *testing.T) {
	info := pluginhost.Info{Steps: []pluginhost.StepInfo{{Name: "a", Export: true}, {Name: "b"}}}

	tests := []struct {
		name string
		step string
		want bool
	}{
		{name: "a step type that exports", step: "a", want: true},
		{name: "a step type that does not", step: "b", want: false},
		{name: "a step type the plugin lacks", step: "missing", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stepExports(info, tt.step))
		})
	}
}

func TestExecWith(t *testing.T) {
	err := execWith([]string{"kevin-no-such-binary"})

	require.ErrorContains(t, err, "do:")
}
