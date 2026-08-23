package main

import (
	"context"

	"github.com/justenwalker/kevin/plugin"
)

// probeStep implements the probe step type. Up always succeeds and creates
// nothing, so it does not implement Downer.
type probeStep struct{}

// Schema constrains the with block of a probe step. A probe step takes no
// configuration.
func (probeStep) Schema() []byte { return nil }

// Kind reports that a probe step is a probe: it creates no resource.
func (probeStep) Kind() plugin.StepKind { return plugin.StepKindProbe }

// probeStep must keep satisfying plugin.Step and plugin.IdempotentStep.
var (
	_ plugin.Step           = probeStep{}
	_ plugin.IdempotentStep = probeStep{}
)

// Idempotent reports that a probe step is safe to call Up on again: it
// creates nothing, so a rerun just checks again.
func (probeStep) Idempotent() bool { return true }

func (probeStep) Up(_ context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	out.Log("stdout", "checked "+req.Step)
	return &plugin.Result{}, nil
}
