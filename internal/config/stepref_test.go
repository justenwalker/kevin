package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseReference(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		want         StepRef
		wantErr      error
		wantContains []string
	}{
		{
			name:  "a valid qualified reference",
			input: "builtin:container",
			want:  StepRef{Plugin: "builtin", Step: "container"},
		},
		{
			name:    "a bare name",
			input:   "container",
			wantErr: ErrBadStepRef,
			wantContains: []string{
				`"container" is not a step type`, // names the value the caller wrote
				"write <plugin>:<step>",          // shows the grammar
				"builtin:container",              // a qualified example built from the caller's value
			},
		},
		{
			name:    "an empty plugin",
			input:   ":container",
			wantErr: ErrBadStepRef,
		},
		{
			name:    "an empty step",
			input:   "builtin:",
			wantErr: ErrBadStepRef,
		},
		{
			name:    "two colons",
			input:   "a:b:c",
			wantErr: ErrBadStepRef,
		},
		{
			name:    "uppercase",
			input:   "Builtin:container",
			wantErr: ErrBadStepRef,
		},
		{
			name:    "a leading hyphen",
			input:   "-builtin:container",
			wantErr: ErrBadStepRef,
		},
		{
			name:    "an empty string",
			input:   "",
			wantErr: ErrBadStepRef,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStepRef(tt.input)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "the error must wrap ErrBadReference")
				for _, s := range tt.wantContains {
					assert.Contains(t, err.Error(), s)
				}
				return
			}
			require.NoError(t, err, "a reference that matches the grammar must parse")
			assert.Equal(t, tt.want, got, "ParseReference must split the plugin from the step")
			assert.Equal(t, tt.input, got.String(), "String must round-trip the input it parsed")
		})
	}
}

func TestReserved(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "builtin", want: true},
		{name: "kevin", want: true},
		{name: "acme", want: false},
		{name: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsReservedName(tt.name),
				"Reserved must report whether kevin owns the plugin name")
		})
	}
}

func TestIsValidIdentifier(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "valid", want: true},
		{name: "valid-w1th-numb3rs", want: true},
		{name: "-starts-with-dash", want: false},
		{name: "ends-with-dash-", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := isValidIdentifier(tt.name)
			if tt.want {
				assert.True(t, b, "valid identifier")
			} else {
				assert.False(t, b, "invalid identifier")
			}
		})
	}
}
