// Package trust installs the kevin authority into the trust stores of the
// machine, and removes it again.
//
// The step belongs in the setup scope.
package trust

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/justenwalker/kevin/internal/ca"
	trustinstall "github.com/justenwalker/kevin/internal/trust"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	System  bool `json:"system"`
	Firefox bool `json:"firefox"`
}

// Step is the trust step.
type Step struct{}

// New returns the trust step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a trust step.
func (Step) Schema() []byte { return schema }

// Kind reports that a trust step creates and destroys a resource.
func (Step) Kind() plugin.StepKind { return plugin.StepKindResource }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a trust step is idempotent. Up and Down both
// treat a store that already matches the desired state as success rather
// than an error.
func (Step) Idempotent() bool { return true }

// Up installs the trust anchor.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	request := requestFor(cfg)
	results, err := trustinstall.Install(ctx, request)
	report(out, results)
	if err != nil {
		return nil, advise(err, results)
	}

	return &plugin.Result{Outputs: plugin.StringMap(map[string]string{
		"common_name": request.CommonName,
		"cert":        request.CertPath,
	})}, nil
}

// Down removes the trust anchor.
func (Step) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}

	results, err := trustinstall.Remove(ctx, requestFor(cfg))
	report(out, results)
	if err != nil {
		return advise(err, results)
	}
	return nil
}

// requestFor builds the trust request for one environment. The store holds
// the machine's root; one entry is shared by every project.
func requestFor(cfg config) trustinstall.Request {
	return trustinstall.Request{
		CertPath:   ca.RootCertPath(),
		CommonName: ca.RootCommonName,
		FileName:   "kevin-local-root",
		System:     cfg.System,
		Firefox:    cfg.Firefox,
	}
}

// report writes one line for each store.
func report(out plugin.Emitter, results []trustinstall.Result) {
	for _, r := range results {
		switch {
		case r.Skipped:
			out.Log("stdout", r.Store+": skipped, "+r.Reason)
		case r.Installed:
			line := r.Store + ": installed"
			if r.Reason != "" {
				line += " (" + r.Reason + ")"
			}
			out.Log("stdout", line)
		default:
			out.Log("stdout", r.Store+": removed")
		}
	}
}

// advise adds the command to run by hand when a store needs root.
func advise(err error, results []trustinstall.Result) error {
	if !errors.Is(err, trustinstall.ErrNeedsRoot) {
		return err
	}
	for _, r := range results {
		if r.Reason != "" {
			return fmt.Errorf("%w: %s", err, r.Reason)
		}
	}
	return err
}

// decode parses the with-block JSON into a config. Firefox defaults to
// true.
func decode(data []byte) (config, error) {
	cfg := config{Firefox: true}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("trust: decode config: %w", err)
	}
	return cfg, nil
}
