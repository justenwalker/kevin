// Package kubectlcmd drives the kubectl command line, on the host rather
// than inside a container.
package kubectlcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "kubectl"

// Available reports whether the kubectl command runs.
func Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("kubectlcmd: %w: %w", ErrUnavailable, err)
	}
	if _, err := run(ctx, nil, "version", "--client"); err != nil {
		return fmt.Errorf("kubectlcmd: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// ApplySpec describes one kubectl apply call. Exactly one of Manifest, Path,
// or Kustomize must be set.
type ApplySpec struct {
	Kubeconfig string
	Context    string
	Namespace  string

	// Manifest is inline YAML, applied through stdin.
	Manifest string

	// Path is a manifest file or directory, applied with -f.
	Path string

	// Kustomize is a kustomization directory, applied with -k.
	Kustomize string

	// ServerSide applies with --server-side.
	ServerSide bool
}

// Apply runs kubectl apply against spec, and returns its standard output.
func Apply(ctx context.Context, spec ApplySpec) (string, error) {
	args, stdin := applyArgs(spec)
	out, err := run(ctx, stdin, args...)
	if err != nil {
		return "", fmt.Errorf("kubectlcmd: apply: %w", err)
	}
	return out, nil
}

func applyArgs(spec ApplySpec) ([]string, io.Reader) {
	args := commonArgs(spec.Kubeconfig, spec.Context, spec.Namespace)
	args = append(args, "apply")

	var stdin io.Reader
	switch {
	case spec.Manifest != "":
		args = append(args, "-f", "-")
		stdin = strings.NewReader(spec.Manifest)
	case spec.Kustomize != "":
		args = append(args, "-k", spec.Kustomize)
	default:
		args = append(args, "-f", spec.Path)
	}

	if spec.ServerSide {
		args = append(args, "--server-side")
	}
	return args, stdin
}

// DeleteSpec describes one kubectl delete call. Exactly one of Manifest,
// Path, or Kustomize must be set.
type DeleteSpec struct {
	Kubeconfig string
	Context    string
	Namespace  string

	// Manifest is inline YAML, deleted through stdin.
	Manifest string

	// Path is a manifest file or directory, deleted with -f.
	Path string

	// Kustomize is a kustomization directory, deleted with -k.
	Kustomize string
}

// Delete runs kubectl delete against spec, and returns its standard output.
func Delete(ctx context.Context, spec DeleteSpec) (string, error) {
	args, stdin := deleteArgs(spec)
	out, err := run(ctx, stdin, args...)
	if err != nil {
		return "", fmt.Errorf("kubectlcmd: delete: %w", err)
	}
	return out, nil
}

func deleteArgs(spec DeleteSpec) ([]string, io.Reader) {
	args := commonArgs(spec.Kubeconfig, spec.Context, spec.Namespace)
	args = append(args, "delete", "--ignore-not-found")

	var stdin io.Reader
	switch {
	case spec.Manifest != "":
		args = append(args, "-f", "-")
		stdin = strings.NewReader(spec.Manifest)
	case spec.Kustomize != "":
		args = append(args, "-k", spec.Kustomize)
	default:
		args = append(args, "-f", spec.Path)
	}
	return args, stdin
}

// WaitSpec describes one kubectl wait call.
type WaitSpec struct {
	Kubeconfig string
	Context    string
	Namespace  string

	// Resource names the target, such as "pod/mypod" or "deployment/api".
	Resource string

	// For is the condition to wait for, passed to --for=.
	For string

	// Timeout bounds this one call. Zero omits --timeout.
	Timeout time.Duration
}

// Wait runs kubectl wait against spec, and returns its standard output.
func Wait(ctx context.Context, spec WaitSpec) (string, error) {
	out, err := run(ctx, nil, waitArgs(spec)...)
	if err != nil {
		return "", fmt.Errorf("kubectlcmd: wait: %w", err)
	}
	return out, nil
}

func waitArgs(spec WaitSpec) []string {
	args := commonArgs(spec.Kubeconfig, spec.Context, spec.Namespace)
	args = append(args, "wait", spec.Resource, "--for="+spec.For)
	if spec.Timeout > 0 {
		args = append(args, "--timeout="+spec.Timeout.String())
	}
	return args
}

// RolloutStatusSpec describes one kubectl rollout status call.
type RolloutStatusSpec struct {
	Kubeconfig string
	Context    string
	Namespace  string

	// Resource names the target, such as "deployment/api".
	Resource string

	// Timeout bounds this one call. Zero omits --timeout.
	Timeout time.Duration
}

// RolloutStatus runs kubectl rollout status against spec, and returns its
// standard output.
func RolloutStatus(ctx context.Context, spec RolloutStatusSpec) (string, error) {
	out, err := run(ctx, nil, rolloutStatusArgs(spec)...)
	if err != nil {
		return "", fmt.Errorf("kubectlcmd: rollout status: %w", err)
	}
	return out, nil
}

func rolloutStatusArgs(spec RolloutStatusSpec) []string {
	args := commonArgs(spec.Kubeconfig, spec.Context, spec.Namespace)
	args = append(args, "rollout", "status", spec.Resource)
	if spec.Timeout > 0 {
		args = append(args, "--timeout="+spec.Timeout.String())
	}
	return args
}

func commonArgs(kubeconfig, kubeContext, namespace string) []string {
	args := []string{"--kubeconfig", kubeconfig}
	if kubeContext != "" {
		args = append(args, "--context", kubeContext)
	}
	if namespace != "" {
		args = append(args, "-n", namespace)
	}
	return args
}

// run calls the kubectl binary and returns the standard output. A nil stdin
// gives the command no standard input.
func run(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdin = stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", notInstalled(fmt.Errorf("kubectl %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("kubectl %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing kubectl binary, so the raw exec.ErrNotFound chain doesn't reach
// the user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "kubectl isn't installed, or isn't on PATH - install it: https://kubernetes.io/docs/tasks/tools/#kubectl")
}
