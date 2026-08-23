package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/config"
)

func TestSplitConnectArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		dash     int
		wantStep string
		wantCmd  []string
		wantErr  bool
	}{
		{name: "nothing", args: nil, dash: -1, wantStep: "", wantCmd: nil},
		{name: "step only", args: []string{"cluster"}, dash: -1, wantStep: "cluster", wantCmd: nil},
		{name: "command only", args: []string{"k9s"}, dash: 0, wantStep: "", wantCmd: []string{"k9s"}},
		{
			name: "step and command", args: []string{"cluster", "kubectl", "get", "pods"}, dash: 1,
			wantStep: "cluster", wantCmd: []string{"kubectl", "get", "pods"},
		},
		{name: "too many step names, no dash", args: []string{"a", "b"}, dash: -1, wantErr: true},
		{name: "too many step names, with dash", args: []string{"a", "b", "cmd"}, dash: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, target, err := splitConnectArgs(tt.args, tt.dash)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantStep, step)
			assert.Equal(t, tt.wantCmd, target)
		})
	}
}

func TestCandidateSteps(t *testing.T) {
	cfg := &config.Config{
		Env: map[string]config.Step{
			"cluster-a": {Uses: "builtin:kind"},
		},
		Setup: map[string]config.Step{
			"cluster-b": {Uses: "builtin:kind"},
		},
	}

	all, err := candidateSteps(cfg, "")
	require.NoError(t, err)
	assert.Len(t, all, 2)

	one, err := candidateSteps(cfg, "cluster-b")
	require.NoError(t, err)
	assert.Equal(t, map[string]config.Step{"cluster-b": {Uses: "builtin:kind"}}, one)

	_, err = candidateSteps(cfg, "missing")
	require.Error(t, err)
}
