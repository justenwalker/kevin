// Package fault installs pumba-style network chaos - delay, jitter,
// packet loss, corruption, duplication, and reordering - on one or more
// containers' network namespaces, via the relay's ApplyFault/ClearFault
// RPCs (see internal/relay). It composes every impairment into a single
// netem qdisc per container, since Linux's netem qdisc supports setting
// all of them at once.
//
// A fault step deploys nothing itself: it only tells the relay how to
// treat a namespace another step already created. needs names which
// steps to affect; containers optionally narrows that down to specific
// containers by name, when a needs step manages more than one (a
// builtin:kind cluster's nodes). Every container is resolved from the
// container info the engine already hands this step on
// UpRequest.Containers - never a raw path, never CEL. It has no Down of
// its own: the engine clears an applied fault at teardown independent of
// whether a step's plugin implements Downer at all, the same way a
// builtin:route step has no Down because it owns no lifecycle either -
// more fundamentally so here, since a plugin process has no channel to
// the relay's control connection at all, only the engine does.
//
// fault has no interaction with builtin:route or the kevin proxy - it
// operates purely at the network-namespace level, orthogonal to whatever
// HTTP routing a project's proxy does.
package fault

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Containers       []string `json:"containers"`
	Interface        string   `json:"interface"`
	DelayMS          int32    `json:"delay_ms"`
	JitterMS         int32    `json:"jitter_ms"`
	LossPercent      float64  `json:"loss_percent"`
	CorruptPercent   float64  `json:"corrupt_percent"`
	DuplicatePercent float64  `json:"duplicate_percent"`
	ReorderPercent   float64  `json:"reorder_percent"`
}

// Step is the fault step.
type Step struct{}

// New returns the fault step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a fault step.
func (Step) Schema() []byte { return schema }

// Kind reports that a fault step is apply-only: it tells the relay how to
// treat a namespace, but owns no lifecycle of its own - the namespace
// belongs to whatever step created it.
func (Step) Kind() plugin.StepKind { return plugin.StepKindAction }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a fault step is idempotent: re-Up-ing with the
// same or a changed config just replaces each netem qdisc, matching
// netem's own tc-replace semantics.
func (Step) Idempotent() bool { return true }

// Up decodes the with block, resolves containers (or every container
// every needs entry reports, when unset) against req.Containers, and
// returns one NetworkFault per resolved container. A single target's ID
// is the step's own name, matching how RegisterCapture's id already
// receives the wiring step's name; more than one appends the container's
// own name, the same "<step>/<name>" convention wireRelay already uses
// for a multi-container RegisterCapture.
func (Step) Up(_ context.Context, req *plugin.UpRequest, _ plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}
	if validateErr := validateImpairment(cfg); validateErr != nil {
		return nil, validateErr
	}

	targets, err := resolveTargets(req.Containers, cfg.Containers)
	if err != nil {
		return nil, err
	}

	faults := make([]plugin.NetworkFault, 0, len(targets))
	for _, t := range targets {
		id := req.Step
		if len(targets) > 1 {
			id = req.Step + "/" + t.Name
		}
		faults = append(faults, plugin.NetworkFault{
			ID:               id,
			NetnsPath:        t.NetnsPath,
			Interface:        cfg.Interface,
			DelayMS:          cfg.DelayMS,
			JitterMS:         cfg.JitterMS,
			LossPercent:      cfg.LossPercent,
			CorruptPercent:   cfg.CorruptPercent,
			DuplicatePercent: cfg.DuplicatePercent,
			ReorderPercent:   cfg.ReorderPercent,
		})
	}
	return &plugin.Result{Faults: faults}, nil
}

// validateImpairment reports ErrNoImpairment unless at least one
// impairment field is set - a with block that names a target but sets no
// impairment would be a meaningless no-op.
func validateImpairment(cfg config) error {
	if cfg.DelayMS == 0 && cfg.LossPercent == 0 && cfg.CorruptPercent == 0 &&
		cfg.DuplicatePercent == 0 && cfg.ReorderPercent == 0 {
		return ErrNoImpairment
	}
	return nil
}

// resolveTargets flattens every needs step's reported containers, then
// either returns all of them (want is empty - the common case: fault
// every container needs resolves to) or exactly the ones want names.
//
// A name matches a container's own Name, or - when a needs step reports
// exactly one container - that step's own name, as a fallback alias. A
// container's own Name always takes precedence over a same-string alias,
// resolved in two passes so processing order can never change which one
// wins.
func resolveTargets(stepContainers []plugin.StepContainers, want []string) ([]plugin.ContainerInfo, error) {
	var all []plugin.ContainerInfo
	byName := make(map[string]plugin.ContainerInfo)
	for _, sc := range stepContainers {
		for _, c := range sc.Containers {
			all = append(all, c)
			byName[c.Name] = c
		}
	}
	for _, sc := range stepContainers {
		if len(sc.Containers) != 1 {
			continue
		}
		if _, exists := byName[sc.Step]; !exists {
			byName[sc.Step] = sc.Containers[0]
		}
	}

	if len(all) == 0 {
		return nil, ErrNoContainers
	}
	if len(want) == 0 {
		return all, nil
	}

	targets := make([]plugin.ContainerInfo, 0, len(want))
	for _, name := range want {
		c, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("fault: containers names %q, which no needs step reports: %w", name, ErrContainerNotFound)
		}
		targets = append(targets, c)
	}
	return targets, nil
}

// decode parses the with-block JSON into a config.
func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("fault: decode config: %w", err)
	}
	return cfg, nil
}
