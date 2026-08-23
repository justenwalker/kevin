// Package helmcmd drives the helm command line, on the host.
package helmcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "helm"

// Available reports whether the helm command runs.
func Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("helmcmd: %w: %w", ErrUnavailable, err)
	}
	if _, err := run(ctx, "version", "--short"); err != nil {
		return fmt.Errorf("helmcmd: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// UpgradeSpec describes one helm upgrade --install call.
type UpgradeSpec struct {
	Kubeconfig string
	Context    string
	Release    string
	Namespace  string

	// Chart is a local path, an oci:// reference, or - when Repo is set - a
	// chart name inside that repo.
	Chart   string
	Repo    string
	Version string

	CreateNamespace bool

	// ValuesFiles are passed as repeated -f flags, in order; a later file
	// wins over an earlier one, per Helm's own merge rule.
	ValuesFiles []string

	PostRenderer     string
	PostRendererArgs []string

	Wait   time.Duration
	Atomic bool
}

// UpgradeInstall runs helm upgrade --install against spec, and returns its
// standard output.
func UpgradeInstall(ctx context.Context, spec UpgradeSpec) (string, error) {
	out, err := run(ctx, upgradeArgs(spec)...)
	if err != nil {
		return "", fmt.Errorf("helmcmd: upgrade --install: %w", err)
	}
	return out, nil
}

func upgradeArgs(spec UpgradeSpec) []string {
	args := []string{
		"upgrade", "--install", spec.Release, spec.Chart,
		"--kubeconfig", spec.Kubeconfig,
	}
	if spec.Context != "" {
		args = append(args, "--kube-context", spec.Context)
	}
	if spec.Repo != "" {
		args = append(args, "--repo", spec.Repo)
	}
	if spec.Version != "" {
		args = append(args, "--version", spec.Version)
	}
	if spec.Namespace != "" {
		args = append(args, "-n", spec.Namespace)
	}
	if spec.CreateNamespace {
		args = append(args, "--create-namespace")
	}
	for _, f := range spec.ValuesFiles {
		args = append(args, "-f", f)
	}
	if spec.PostRenderer != "" {
		args = append(args, "--post-renderer", spec.PostRenderer)
		for _, a := range spec.PostRendererArgs {
			args = append(args, "--post-renderer-args", a)
		}
	}
	if spec.Wait > 0 {
		args = append(args, "--wait", "--timeout", spec.Wait.String())
	}
	if spec.Atomic {
		args = append(args, "--atomic")
	}
	return args
}

// UninstallSpec describes one helm uninstall call.
type UninstallSpec struct {
	Kubeconfig string
	Context    string
	Release    string
	Namespace  string
}

// Uninstall runs helm uninstall against spec, and returns its standard
// output. It reports ErrReleaseNotFound, not helm's own exit error, when the
// release is already gone.
func Uninstall(ctx context.Context, spec UninstallSpec) (string, error) {
	out, err := run(ctx, uninstallArgs(spec)...)
	if err != nil {
		if isReleaseNotFound(err) {
			return "", fmt.Errorf("helmcmd: uninstall: %w", ErrReleaseNotFound)
		}
		return "", fmt.Errorf("helmcmd: uninstall: %w", err)
	}
	return out, nil
}

// isReleaseNotFound reports whether err is helm's own exit error for a
// release that is already gone.
func isReleaseNotFound(err error) bool {
	return strings.Contains(err.Error(), "release: not found")
}

func uninstallArgs(spec UninstallSpec) []string {
	args := []string{"uninstall", spec.Release, "--kubeconfig", spec.Kubeconfig}
	if spec.Context != "" {
		args = append(args, "--kube-context", spec.Context)
	}
	if spec.Namespace != "" {
		args = append(args, "-n", spec.Namespace)
	}
	return args
}

// run calls the helm binary and returns the standard output.
func run(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", notInstalled(fmt.Errorf("helm %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("helm %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing helm binary, so the raw exec.ErrNotFound chain doesn't reach the
// user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "helm isn't installed, or isn't on PATH - install it: https://helm.sh/docs/intro/install/")
}
