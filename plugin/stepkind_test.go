package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestStepKindString(t *testing.T) {
	tests := []struct {
		name string
		kind StepKind
		want string
	}{
		{name: "unspecified", kind: StepKindUnspecified, want: ""},
		{name: "resource", kind: StepKindResource, want: "resource"},
		{name: "action", kind: StepKindAction, want: "action"},
		{name: "probe", kind: StepKindProbe, want: "probe"},
		{name: "an unknown kind", kind: StepKind(99), want: "StepKind(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.kind.String())
		})
	}
}

func TestStepKindToProto(t *testing.T) {
	tests := []struct {
		name string
		kind StepKind
		want pb.StepKind
	}{
		{name: "unspecified", kind: StepKindUnspecified, want: pb.StepKind_STEP_KIND_UNSPECIFIED},
		{name: "resource", kind: StepKindResource, want: pb.StepKind_STEP_KIND_RESOURCE},
		{name: "action", kind: StepKindAction, want: pb.StepKind_STEP_KIND_ACTION},
		{name: "probe", kind: StepKindProbe, want: pb.StepKind_STEP_KIND_PROBE},
		{name: "an unknown kind maps to unspecified", kind: StepKind(99), want: pb.StepKind_STEP_KIND_UNSPECIFIED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stepKindToProto(tt.kind))
		})
	}
}
