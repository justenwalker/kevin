package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRunExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{
			name: "an unknown command is a usage error",
			args: []string{"kevin", "nonsense"},
			want: 2,
		},
		{
			name: "an unknown flag is a usage error",
			args: []string{"kevin", "run", "--nonsense"},
			want: 2,
		},
		{
			name: "a command failure is not a usage error",
			args: []string{"kevin", "-C", t.TempDir(), "teardown"},
			want: 1,
		},
		{
			name: "a missing kevin.cue is not a usage error",
			args: []string{"kevin", "-C", t.TempDir(), "run"},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, run(t.Context(), tt.args))
		})
	}
}
