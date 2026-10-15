package plugin

import (
	"context"
	"encoding/json"
	"fmt"
)

// TypedStep is a [Step] whose with block decodes into C. The SDK decodes the
// block, so Up receives it already parsed. Every default the step type's CUE
// schema declares is already in the block.
type TypedStep[C any] interface {
	// Kind classifies what this step type is. See [Step.Kind].
	Kind() StepKind

	// Up executes the step. See [Step.Up].
	Up(ctx context.Context, cfg C, req *UpRequest, out Emitter) (*Result, error)
}

// TypedDowner is a [TypedStep] that also removes what it created. See
// [Downer].
type TypedDowner[C any] interface {
	Down(ctx context.Context, cfg C, req *DownRequest, out Emitter) error
}

// TypedExporter is a [TypedStep] that also reports how to reach what it
// created. See [Exporter].
type TypedExporter[C any] interface {
	Export(ctx context.Context, cfg C, req *ExportRequest) (*ExportResult, error)
}

// TypedToolProvider is a [TypedStep] that also offers MCP tools. See
// [ToolProvider].
type TypedToolProvider[C any] interface {
	Tools() []ToolDef
	CallTool(ctx context.Context, cfg C, req *ToolCallRequest) (*ToolCallResult, error)
}

// Typed adapts s to a [Step]. schema is the CUE schema of the with block.
// Typed passes Down, Export, CallTool and Idempotent through when s
// implements the typed form of each, and the plugin reports only those.
func Typed[C any](schema []byte, s TypedStep[C]) Step { //nolint:ireturn // Step is the SDK's step contract
	return &typedStep[C]{schema: schema, step: s}
}

// capability is an optional part of a [Step] that a [typedStep] has only when
// the step it wraps does.
type capability uint8

const (
	capDown capability = iota
	capExport
	capTools
)

// capable is implemented by a step that only has some of the optional parts
// its method set shows.
type capable interface {
	supports(c capability) bool
}

// typedStep is the [Step] that [Typed] returns.
type typedStep[C any] struct {
	schema []byte
	step   TypedStep[C]
}

var (
	_ Step           = (*typedStep[struct{}])(nil)
	_ Downer         = (*typedStep[struct{}])(nil)
	_ Exporter       = (*typedStep[struct{}])(nil)
	_ ToolProvider   = (*typedStep[struct{}])(nil)
	_ IdempotentStep = (*typedStep[struct{}])(nil)
	_ capable        = (*typedStep[struct{}])(nil)
)

func (t *typedStep[C]) Schema() []byte { return t.schema }

func (t *typedStep[C]) Kind() StepKind { return t.step.Kind() }

func (t *typedStep[C]) supports(c capability) bool {
	switch c {
	case capDown:
		_, ok := t.step.(TypedDowner[C])
		return ok
	case capExport:
		_, ok := t.step.(TypedExporter[C])
		return ok
	case capTools:
		_, ok := t.step.(TypedToolProvider[C])
		return ok
	default:
		return false
	}
}

func (t *typedStep[C]) Idempotent() bool {
	idem, ok := t.step.(IdempotentStep)
	return ok && idem.Idempotent()
}

func (t *typedStep[C]) Up(ctx context.Context, req *UpRequest, out Emitter) (*Result, error) {
	cfg, err := decodeWith[C](req.Type, req.Config)
	if err != nil {
		return nil, err
	}
	return t.step.Up(ctx, cfg, req, out)
}

func (t *typedStep[C]) Down(ctx context.Context, req *DownRequest, out Emitter) error {
	downer, ok := t.step.(TypedDowner[C])
	if !ok {
		return fmt.Errorf("plugin: step type %q: %w", req.Type, ErrNotImplemented)
	}
	cfg, err := decodeWith[C](req.Type, req.Config)
	if err != nil {
		return err
	}
	return downer.Down(ctx, cfg, req, out)
}

func (t *typedStep[C]) Export(ctx context.Context, req *ExportRequest) (*ExportResult, error) {
	exporter, ok := t.step.(TypedExporter[C])
	if !ok {
		return nil, fmt.Errorf("plugin: step type %q: %w", req.Type, ErrNotImplemented)
	}
	cfg, err := decodeWith[C](req.Type, req.Config)
	if err != nil {
		return nil, err
	}
	return exporter.Export(ctx, cfg, req)
}

func (t *typedStep[C]) Tools() []ToolDef {
	if tools, ok := t.step.(TypedToolProvider[C]); ok {
		return tools.Tools()
	}
	return nil
}

func (t *typedStep[C]) CallTool(ctx context.Context, req *ToolCallRequest) (*ToolCallResult, error) {
	tools, ok := t.step.(TypedToolProvider[C])
	if !ok {
		return nil, fmt.Errorf("plugin: step type %q: %w", req.Type, ErrNotImplemented)
	}
	cfg, err := decodeWith[C](req.Type, req.Config)
	if err != nil {
		return nil, err
	}
	return tools.CallTool(ctx, cfg, req)
}

// decodeWith decodes a step's with block into C. An empty block is the zero
// C.
func decodeWith[C any](stepType string, with []byte) (C, error) { //nolint:ireturn // C is the caller's own config type
	var cfg C
	if len(with) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(with, &cfg); err != nil {
		return cfg, fmt.Errorf("plugin: step type %q: decode the with block: %w", stepType, err)
	}
	return cfg, nil
}
