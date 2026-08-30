package main

import (
	"context"
	_ "embed"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/justenwalker/kevin/plugin"
)

// version holds the version string. The build sets this value.
var version = "v0.0.1"

//go:embed schema.cue
var schema []byte

type echo struct{}

// echo must keep satisfying plugin.Step.
var _ plugin.Step = echo{}

func (echo) Schema() []byte { return schema }

func (echo) Kind() plugin.StepKind { return plugin.StepKindResource }

func (echo) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	if cfg.Message != "" {
		out.Log("stdout", cfg.Message)
	}
	if g := currentGreeting(); g != "" {
		out.Log("stdout", "provider greeting: "+g)
	}
	for _, dep := range slices.Sorted(maps.Keys(req.Deps)) {
		out.Log("stdout", fmt.Sprintf("saw %s: %v", dep, req.Deps[dep]))
	}

	if cfg.Delay != "" {
		d, parseErr := time.ParseDuration(cfg.Delay)
		if parseErr != nil {
			return nil, fmt.Errorf("echo: delay %q: %w", cfg.Delay, parseErr)
		}
		out.Progress("waiting", 0, 0)
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
	}

	if cfg.Fail {
		return nil, ErrRequested
	}

	outputs := cfg.Outputs
	if outputs == nil {
		outputs = map[string]string{}
	}
	outputs["step"] = req.Step

	details := make([]plugin.Detail, 0, len(cfg.Details))
	for _, d := range cfg.Details {
		details = append(details, plugin.Detail{Label: d.Label, Value: plugin.String(d.Value), Copyable: d.Copyable, Href: d.Href})
	}

	return &plugin.Result{Outputs: plugin.StringMap(outputs), Details: details}, nil
}

// Down logs "removing <step>", then cfg.Message if the with block sets one.
func (echo) Down(_ context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	out.Log("stdout", "removing "+req.Step)
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}
	if cfg.Message != "" {
		out.Log("stdout", cfg.Message)
	}
	return nil
}

// echo must keep satisfying plugin.Exporter.
var _ plugin.Exporter = echo{}

// Export reports cfg.Export, with any key named in cfg.ExportSensitive
// wrapped plugin.Sensitive.
func (echo) Export(_ context.Context, req *plugin.ExportRequest) (*plugin.ExportResult, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}
	out := plugin.StringMap(cfg.Export)
	if out == nil {
		out = make(map[string]plugin.Value, 1)
	}
	for _, k := range cfg.ExportSensitive {
		if v, ok := out[k]; ok {
			out[k] = plugin.Sensitive{Value: v}
		}
	}
	// exportCalls lets a test prove a caller memoizes Export instead of
	// calling it once per consumer.
	out["export_calls"] = plugin.String(strconv.Itoa(int(exportCalls.Add(1))))
	return &plugin.ExportResult{Out: out}, nil
}

// exportCalls counts every Export call this process has served.
var exportCalls atomic.Int64
