// Package helm installs or upgrades a Helm release on an existing
// Kubernetes cluster, using the host helm binary. Down uninstalls the
// release unless the step sets keep, which leaves it in place.
package helm

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/command"
	helmcmd "github.com/justenwalker/kevin/internal/command/helm"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// config is the decoded with block of one step.
type config struct {
	Kubeconfig       string   `json:"kubeconfig"`
	Context          string   `json:"context"`
	Release          string   `json:"release"`
	Namespace        string   `json:"namespace"`
	CreateNamespace  bool     `json:"create_namespace"`
	Chart            string   `json:"chart"`
	Repo             string   `json:"repo"`
	Version          string   `json:"version"`
	ValuesFiles      []string `json:"values_files"`
	PostRenderer     string   `json:"post_renderer"`
	PostRendererArgs []string `json:"post_renderer_args"`
	Wait             string   `json:"wait"`
	Atomic           bool     `json:"atomic"`
	Keep             bool     `json:"keep"`
}

// cli is the part of the helm command line that a Step calls.
type cli interface {
	UpgradeInstall(ctx context.Context, spec helmcmd.UpgradeSpec) (string, error)
	Uninstall(ctx context.Context, spec helmcmd.UninstallSpec) (string, error)
}

// Step is the helm step.
type Step struct {
	cli cli
}

// New returns the helm step, running helm with [command.Default].
func New() plugin.Step { return plugin.Typed(schema, Step{cli: helmcmd.New(command.Default)}) } //nolint:ireturn // the step is the SDK contract

// Step must keep satisfying plugin.TypedStep.
var _ plugin.TypedStep[config] = Step{}

// Kind reports that a helm step performs an action.
func (Step) Kind() plugin.StepKind { return plugin.StepKindAction }

// Step must keep satisfying plugin.TypedDowner.
var _ plugin.TypedDowner[config] = Step{}

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a helm step is idempotent. Up always runs helm
// upgrade --install, which reconciles the release instead of erroring
// when it already exists.
func (Step) Idempotent() bool { return true }

// Up installs or upgrades the release.
func (s Step) Up(ctx context.Context, cfg config, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	wait, err := parseDuration(cfg.Wait)
	if err != nil {
		return nil, fmt.Errorf("helm: wait %q: %w", cfg.Wait, err)
	}

	values := make([]string, len(cfg.ValuesFiles))
	for i, f := range cfg.ValuesFiles {
		values[i] = resolvePath(f, req.Env.ProjectDir)
	}

	out.Log("stdout", "upgrading release "+cfg.Release)
	if _, err = s.cli.UpgradeInstall(ctx, helmcmd.UpgradeSpec{
		Kubeconfig:       cfg.Kubeconfig,
		Context:          cfg.Context,
		Release:          cfg.Release,
		Namespace:        cfg.Namespace,
		CreateNamespace:  cfg.CreateNamespace,
		Chart:            resolveChart(cfg.Chart, cfg.Repo, req.Env.ProjectDir),
		Repo:             cfg.Repo,
		Version:          cfg.Version,
		ValuesFiles:      values,
		PostRenderer:     cfg.PostRenderer,
		PostRendererArgs: cfg.PostRendererArgs,
		Wait:             wait,
		Atomic:           cfg.Atomic,
	}); err != nil {
		return nil, fmt.Errorf("helm: %w", err)
	}
	out.Log("stdout", "release "+cfg.Release+" ready")

	return &plugin.Result{
		Outputs: plugin.StringMap(map[string]string{
			"release":    cfg.Release,
			"namespace":  cfg.Namespace,
			"kubeconfig": cfg.Kubeconfig,
			"context":    cfg.Context,
		}),
	}, nil
}

// Down uninstalls the release. If the step's with block sets keep, Down
// does nothing and the release stays installed.
func (s Step) Down(ctx context.Context, cfg config, req *plugin.DownRequest, out plugin.Emitter) error {
	if cfg.Keep {
		out.Log("stdout", "keeping release "+cfg.Release)
		return nil
	}

	out.Log("stdout", "uninstalling release "+cfg.Release)
	if _, err := s.cli.Uninstall(ctx, helmcmd.UninstallSpec{
		Kubeconfig: cfg.Kubeconfig,
		Context:    cfg.Context,
		Release:    cfg.Release,
		Namespace:  cfg.Namespace,
	}); err != nil {
		if errors.Is(err, helmcmd.ErrReleaseNotFound) {
			out.Log("stdout", "release "+cfg.Release+" already gone")
			return nil
		}
		return fmt.Errorf("helm: %w", err)
	}
	out.Log("stdout", "release "+cfg.Release+" uninstalled")
	return nil
}

// parseDuration reads the wait duration. It returns 0 if s is empty.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s) //nolint:wrapcheck // caller wraps
}

// resolveChart resolves chart as a local path, unless repo is set (chart is
// then a name inside that repo) or chart already carries a URL scheme, such
// as oci://.
func resolveChart(chart, repo, projectDir string) string {
	if repo != "" || strings.Contains(chart, "://") {
		return chart
	}
	return resolvePath(chart, projectDir)
}

// resolvePath resolves a path against the project directory.
// An absolute path, an empty path, or a missing project directory pass through
// unchanged.
func resolvePath(path, projectDir string) string {
	if path == "" || projectDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectDir, path)
}
