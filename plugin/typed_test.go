package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/protos/pb"
)

type typedConfig struct {
	Image string `json:"image"`
}

// minimalTyped has only Kind and Up.
type minimalTyped struct{ got *typedConfig }

func (minimalTyped) Kind() StepKind { return StepKindAction }

func (m minimalTyped) Up(_ context.Context, cfg typedConfig, _ *UpRequest, _ Emitter) (*Result, error) {
	*m.got = cfg
	return &Result{}, nil
}

// fullTyped has every optional part.
type fullTyped struct {
	minimalTyped

	downCfg   *typedConfig
	exportCfg *typedConfig
	toolCfg   *typedConfig
}

func (f fullTyped) Idempotent() bool { return true }

func (f fullTyped) Down(_ context.Context, cfg typedConfig, _ *DownRequest, _ Emitter) error {
	*f.downCfg = cfg
	return nil
}

func (f fullTyped) Export(_ context.Context, cfg typedConfig, _ *ExportRequest) (*ExportResult, error) {
	*f.exportCfg = cfg
	return &ExportResult{}, nil
}

func (fullTyped) Tools() []ToolDef { return []ToolDef{{Name: "t"}} }

func (f fullTyped) CallTool(_ context.Context, cfg typedConfig, _ *ToolCallRequest) (*ToolCallResult, error) {
	*f.toolCfg = cfg
	return &ToolCallResult{}, nil
}

func TestTyped(t *testing.T) {
	t.Run("Up decodes the with block", func(t *testing.T) {
		var got typedConfig
		step := Typed[typedConfig]([]byte("#Config: {}"), minimalTyped{got: &got})

		_, err := step.Up(t.Context(), &UpRequest{Type: "x", Config: []byte(`{"image":"nginx"}`)}, nil)

		require.NoError(t, err)
		assert.Equal(t, "nginx", got.Image)
		assert.Equal(t, []byte("#Config: {}"), step.Schema())
		assert.Equal(t, StepKindAction, step.Kind())
	})

	t.Run("an empty with block is the zero config", func(t *testing.T) {
		got := typedConfig{Image: "stale"}
		step := Typed[typedConfig](nil, minimalTyped{got: &got})

		_, err := step.Up(t.Context(), &UpRequest{Type: "x"}, nil)

		require.NoError(t, err)
		assert.Empty(t, got.Image)
	})

	t.Run("a with block that does not decode names the step type", func(t *testing.T) {
		var got typedConfig
		step := Typed[typedConfig](nil, minimalTyped{got: &got})

		_, err := step.Up(t.Context(), &UpRequest{Type: "widget", Config: []byte(`{`)}, nil)

		require.ErrorContains(t, err, `step type "widget"`)
	})

	t.Run("a step with only Up reports no optional parts", func(t *testing.T) {
		var got typedConfig
		srv := &server{provider: Plugin{Steps: map[string]Step{"x": Typed[typedConfig](nil, minimalTyped{got: &got})}}}

		info, err := srv.Info(t.Context(), &pb.InfoRequest{})

		require.NoError(t, err)
		st := info.GetSteps()[0]
		assert.False(t, st.GetDown())
		assert.False(t, st.GetExport())
		assert.False(t, st.GetIdempotent())
		assert.Empty(t, st.GetTools())
	})

	t.Run("a step with every part reports each", func(t *testing.T) {
		var a, b, c, d typedConfig
		full := fullTyped{minimalTyped: minimalTyped{got: &a}, downCfg: &b, exportCfg: &c, toolCfg: &d}
		srv := &server{provider: Plugin{Steps: map[string]Step{"x": Typed[typedConfig](nil, full)}}}

		info, err := srv.Info(t.Context(), &pb.InfoRequest{})

		require.NoError(t, err)
		st := info.GetSteps()[0]
		assert.True(t, st.GetDown())
		assert.True(t, st.GetExport())
		assert.True(t, st.GetIdempotent())
		require.Len(t, st.GetTools(), 1)
	})

	t.Run("Down, Export and CallTool decode the with block", func(t *testing.T) {
		var a, down, export, tool typedConfig
		full := fullTyped{minimalTyped: minimalTyped{got: &a}, downCfg: &down, exportCfg: &export, toolCfg: &tool}
		step := Typed[typedConfig](nil, full)
		with := []byte(`{"image":"i"}`)

		downer, ok := step.(Downer)
		require.True(t, ok)
		exporter, ok := step.(Exporter)
		require.True(t, ok)
		tools, ok := step.(ToolProvider)
		require.True(t, ok)

		require.NoError(t, downer.Down(t.Context(), &DownRequest{Type: "x", Config: with}, nil))
		_, err := exporter.Export(t.Context(), &ExportRequest{Type: "x", Config: with})
		require.NoError(t, err)
		_, err = tools.CallTool(t.Context(), &ToolCallRequest{Type: "x", Config: with})
		require.NoError(t, err)

		assert.Equal(t, "i", down.Image)
		assert.Equal(t, "i", export.Image)
		assert.Equal(t, "i", tool.Image)
	})

	t.Run("the server refuses Down on a step that has none", func(t *testing.T) {
		var got typedConfig
		srv := &server{provider: Plugin{Steps: map[string]Step{"x": Typed[typedConfig](nil, minimalTyped{got: &got})}}}

		err := srv.Down(&pb.DownRequest{Type: "x"}, nil)

		require.Error(t, err)
	})
}
