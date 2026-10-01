// Package minikubecmd drives the minikube command line, on the host.
package minikubecmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "minikube"

// Available reports whether the minikube command runs.
func Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("minikubecmd: %w: %w", ErrUnavailable, err)
	}
	if _, err := runBuffered(ctx, nil, "version", "--short"); err != nil {
		return fmt.Errorf("minikubecmd: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// StartSpec describes one minikube start call.
type StartSpec struct {
	Name string

	// Driver is the minikube driver, "docker" or "podman".
	Driver string

	// Network names an existing network that every node joins. Empty leaves
	// minikube to pick one.
	Network string

	// Nodes is the node count, control plane included.
	Nodes int

	Wait time.Duration

	// KubernetesVersion is such as "v1.33.1". Empty uses the default of the
	// installed minikube.
	KubernetesVersion string

	// BaseImage is the node image. Empty uses the default of the installed
	// minikube.
	BaseImage string

	// Memory limits the memory of every node, such as "2g". Empty leaves
	// minikube to choose.
	Memory string

	// CPUs limits the CPUs of every node. Zero leaves minikube to choose.
	CPUs int

	// Mount is one "host:container" bind mount, with an optional ":ro"
	// suffix, made on every node.
	Mount string

	// Home is the MINIKUBE_HOME of the call.
	Home string

	// Kubeconfig is the file that minikube writes the cluster's credentials
	// to. Empty leaves minikube to use the user's own.
	Kubeconfig string
}

// Start runs minikube start against spec, streaming its output to stdout and
// stderr as it runs.
func Start(ctx context.Context, spec StartSpec, stdout, stderr io.Writer) error {
	if err := runStreamed(ctx, stdout, stderr, spec.Home, spec.Kubeconfig, StartArgs(spec)...); err != nil {
		return fmt.Errorf("minikubecmd: start: %w", err)
	}
	return nil
}

// StartArgs builds the argument list of the minikube start call for spec.
// Home and Kubeconfig are not arguments: they reach minikube through its
// environment.
func StartArgs(spec StartSpec) []string {
	args := []string{
		"start", "-p", spec.Name,
		"--driver=" + spec.Driver,
		"--container-runtime=containerd",
		"--nodes=" + strconv.Itoa(spec.Nodes),
		"--wait=all",
		"--embed-certs",
		"--interactive=false",
	}
	if spec.Wait > 0 {
		args = append(args, "--wait-timeout="+spec.Wait.String())
	}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	if spec.KubernetesVersion != "" {
		args = append(args, "--kubernetes-version", spec.KubernetesVersion)
	}
	if spec.BaseImage != "" {
		args = append(args, "--base-image", spec.BaseImage)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.Itoa(spec.CPUs))
	}
	if spec.Mount != "" {
		args = append(args, "--mount", "--mount-string", spec.Mount)
	}
	return args
}

// Delete runs minikube delete against profile name, streaming its output to
// stderr as it runs. A profile that does not exist is not an error.
func Delete(ctx context.Context, name, home string, stderr io.Writer) error {
	if err := runStreamed(ctx, io.Discard, stderr, home, "", "delete", "-p", name); err != nil {
		return fmt.Errorf("minikubecmd: delete: %w", err)
	}
	return nil
}

// ImageLoad runs minikube image load, loading the tar file at path into
// every node of profile name.
func ImageLoad(ctx context.Context, name, home, path string, stderr io.Writer) error {
	if err := runStreamed(ctx, io.Discard, stderr, home, "", "image", "load", path, "-p", name); err != nil {
		return fmt.Errorf("minikubecmd: image load: %w", err)
	}
	return nil
}

// proxyVariables are the variables that minikube reads from its own
// environment and pushes into the nodes, and that its host downloads would
// also use.
var proxyVariables = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
	"http_proxy", "https_proxy", "no_proxy", "all_proxy",
}

// Env returns the environment of a minikube call: the process's own, without
// any proxy variable, plus the variables that keep minikube's state under
// home and its credentials in kubeconfig. An empty kubeconfig leaves
// KUBECONFIG as the process's own.
func Env(home, kubeconfig string) []string {
	drop := map[string]bool{
		"MINIKUBE_HOME":                   true,
		"MINIKUBE_IN_STYLE":               true,
		"MINIKUBE_WANTUPDATENOTIFICATION": true,
	}
	for _, key := range proxyVariables {
		drop[key] = true
	}
	if kubeconfig != "" {
		drop["KUBECONFIG"] = true
	}

	var env []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !drop[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"MINIKUBE_HOME="+home,
		"MINIKUBE_IN_STYLE=false",
		"MINIKUBE_WANTUPDATENOTIFICATION=false",
	)
	if kubeconfig != "" {
		env = append(env, "KUBECONFIG="+kubeconfig)
	}
	return env
}

// runBuffered calls the minikube binary and returns its standard output, for
// a call whose result is data to parse rather than progress to show. A nil
// env leaves the child's environment as the process's own.
func runBuffered(ctx context.Context, env []string, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	if env != nil {
		cmd.Env = env
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", notInstalled(fmt.Errorf("minikube %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("minikube %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing minikube binary, so the raw exec.ErrNotFound chain doesn't reach
// the user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "minikube isn't installed, or isn't on PATH - install it: https://minikube.sigs.k8s.io/docs/start/")
}

// runStreamed calls the minikube binary with stdout and stderr wired
// straight through to the caller's writers, for a call long enough that the
// caller needs to see progress as it happens rather than only once it exits.
func runStreamed(ctx context.Context, stdout, stderr io.Writer, home, kubeconfig string, args ...string) error {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = Env(home, kubeconfig)

	if err := cmd.Run(); err != nil {
		return notInstalled(fmt.Errorf("minikube %s: %w", strings.Join(args, " "), err))
	}
	return nil
}
