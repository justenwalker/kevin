package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
