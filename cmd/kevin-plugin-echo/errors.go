package main

import (
	"context"

	"github.com/justenwalker/kevin/plugin"
)

// Error is a constant sentinel error.
type Error string

func (e Error) Error() string { return string(e) }

// ErrRequested reports the failure that `fail: true` asks for.
const ErrRequested = Error("echo: failure requested by configuration")

// failStep implements the fail step type. Up always fails, so a `needs`
// edge on it can be used to test partial-environment teardown.
type failStep struct{}

// failStep must keep satisfying plugin.Step.
var _ plugin.Step = failStep{}

func (failStep) Schema() []byte { return nil }

func (failStep) Kind() plugin.StepKind { return plugin.StepKindAction }

func (failStep) Up(context.Context, *plugin.UpRequest, plugin.Emitter) (*plugin.Result, error) {
	return nil, plugin.Wrap(ErrRequested, "the fail step always fails, on purpose")
}
