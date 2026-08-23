// Package kubectl applies a manifest, a manifest directory, or a
// kustomization to an existing Kubernetes cluster, using the host kubectl
// binary. Down deletes what was applied, unless the step sets keep, which
// leaves it in place.
package kubectl

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/justenwalker/kevin/internal/kubectlcmd"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
	Namespace  string `json:"namespace"`
	Manifest   string `json:"manifest"`
	Path       string `json:"path"`
	Kustomize  string `json:"kustomize"`
	ServerSide bool   `json:"server_side"`
	Keep       bool   `json:"keep"`
}

// Step is the kubectl step.
type Step struct{}

// New returns the kubectl step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a kubectl step.
func (Step) Schema() []byte { return schema }

// Kind reports that a kubectl step performs an action.
func (Step) Kind() plugin.StepKind { return plugin.StepKindAction }

// Step must keep satisfying plugin.Downer.
var _ plugin.Downer = Step{}

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a kubectl step is idempotent. Up always runs
// kubectl apply, which reconciles the cluster to match the manifest
// instead of erroring or duplicating resources on a rerun.
func (Step) Idempotent() bool { return true }

// Up applies the manifest.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}
	if err = validateSource(cfg); err != nil {
		return nil, err
	}

	out.Log("stdout", "applying "+applyLabel(cfg))
	if _, err = kubectlcmd.Apply(ctx, kubectlcmd.ApplySpec{
		Kubeconfig: cfg.Kubeconfig,
		Context:    cfg.Context,
		Namespace:  cfg.Namespace,
		Manifest:   cfg.Manifest,
		Path:       resolvePath(cfg.Path, req.Env.ProjectDir),
		Kustomize:  resolvePath(cfg.Kustomize, req.Env.ProjectDir),
		ServerSide: cfg.ServerSide,
	}); err != nil {
		return nil, fmt.Errorf("kubectl: %w", err)
	}
	out.Log("stdout", "applied")

	return &plugin.Result{
		Outputs: plugin.StringMap(map[string]string{
			"kubeconfig": cfg.Kubeconfig,
			"context":    cfg.Context,
			"namespace":  cfg.Namespace,
		}),
	}, nil
}

// Down deletes what Up applied. If the step's with block sets keep, Down
// does nothing and the resources stay in place.
func (Step) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}
	if err = validateSource(cfg); err != nil {
		return err
	}
	if cfg.Keep {
		out.Log("stdout", "keeping "+applyLabel(cfg))
		return nil
	}

	out.Log("stdout", "deleting "+applyLabel(cfg))
	if _, err = kubectlcmd.Delete(ctx, kubectlcmd.DeleteSpec{
		Kubeconfig: cfg.Kubeconfig,
		Context:    cfg.Context,
		Namespace:  cfg.Namespace,
		Manifest:   cfg.Manifest,
		Path:       resolvePath(cfg.Path, req.Env.ProjectDir),
		Kustomize:  resolvePath(cfg.Kustomize, req.Env.ProjectDir),
	}); err != nil {
		return fmt.Errorf("kubectl: %w", err)
	}
	out.Log("stdout", "deleted")
	return nil
}

// validateSource reports ErrSource unless exactly one of manifest, path,
// kustomize is set.
func validateSource(cfg config) error {
	n := 0
	for _, s := range []string{cfg.Manifest, cfg.Path, cfg.Kustomize} {
		if s != "" {
			n++
		}
	}
	if n != 1 {
		return ErrSource
	}
	return nil
}

func applyLabel(cfg config) string {
	switch {
	case cfg.Kustomize != "":
		return "kustomization " + cfg.Kustomize
	case cfg.Path != "":
		return cfg.Path
	default:
		return "inline manifest"
	}
}

// resolvePath resolves a with-block path against the project directory. An
// absolute path, an empty path, or a missing project directory pass through
// unchanged.
func resolvePath(path, projectDir string) string {
	if path == "" || projectDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectDir, path)
}

// decode parses the with-block JSON into a config.
func decode(data []byte) (config, error) {
	var cfg config
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("kubectl: decode config: %w", err)
	}
	return cfg, nil
}
