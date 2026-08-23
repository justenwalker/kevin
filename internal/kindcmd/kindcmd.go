// Package kindcmd drives the kind command line, on the host.
package kindcmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "kind"

// Available reports whether the kind command runs.
func Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("kindcmd: %w: %w", ErrUnavailable, err)
	}
	if _, err := runBuffered(ctx, "version"); err != nil {
		return fmt.Errorf("kindcmd: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// CreateSpec describes one kind create cluster call.
type CreateSpec struct {
	Name       string
	Kubeconfig string

	// Config is the raw kind cluster config YAML, fed on stdin.
	Config string

	Wait   time.Duration
	Retain bool
	Image  string

	// Env names extra variables (HTTP_PROXY, HTTPS_PROXY, NO_PROXY, ...) set
	// for this call only, layered onto the process's own environment - never
	// the process's own environment itself.
	Env map[string]string
}

// Create runs kind create cluster against spec, streaming its output to
// stdout and stderr as it runs - a cluster can take minutes to come up, so
// the caller sees progress rather than one message at the end.
func Create(ctx context.Context, spec CreateSpec, stdout, stderr io.Writer) error {
	args := createArgs(spec)
	if err := runStreamed(ctx, strings.NewReader(spec.Config), stdout, stderr, envWith(spec.Env), args...); err != nil {
		return fmt.Errorf("kindcmd: create cluster: %w", err)
	}
	return nil
}

func createArgs(spec CreateSpec) []string {
	args := []string{
		"create", "cluster",
		"--name", spec.Name,
		"--config", "-",
		"--kubeconfig", spec.Kubeconfig,
	}
	if spec.Wait > 0 {
		args = append(args, "--wait", spec.Wait.String())
	}
	if spec.Retain {
		args = append(args, "--retain")
	}
	if spec.Image != "" {
		args = append(args, "--image", spec.Image)
	}
	return args
}

// DeleteSpec describes one kind delete cluster call.
type DeleteSpec struct {
	Name       string
	Kubeconfig string
}

// Delete runs kind delete cluster against spec, streaming its output to
// stderr as it runs. kind documents delete cluster as idempotent - a
// cluster that is already gone is success, not an error.
func Delete(ctx context.Context, spec DeleteSpec, stderr io.Writer) error {
	args := deleteArgs(spec)
	if err := runStreamed(ctx, nil, io.Discard, stderr, nil, args...); err != nil {
		return fmt.Errorf("kindcmd: delete cluster: %w", err)
	}
	return nil
}

func deleteArgs(spec DeleteSpec) []string {
	return []string{"delete", "cluster", "--name", spec.Name, "--kubeconfig", spec.Kubeconfig}
}

// GetNodes runs kind get nodes against name, and returns the docker
// container name of every node in the cluster.
func GetNodes(ctx context.Context, name string) ([]string, error) {
	out, err := runBuffered(ctx, "get", "nodes", "--name", name)
	if err != nil {
		return nil, fmt.Errorf("kindcmd: get nodes: %w", err)
	}
	return parseLines(out), nil
}

// GetClusters runs kind get clusters, and returns the name of every kind
// cluster on the host - not scoped to kevin's own, the same as the CLI
// itself. Test-only: production code always knows a cluster's deterministic
// name already and never needs to enumerate every cluster on the host.
func GetClusters(ctx context.Context) ([]string, error) {
	out, err := runBuffered(ctx, "get", "clusters")
	if err != nil {
		return nil, fmt.Errorf("kindcmd: get clusters: %w", err)
	}
	return parseLines(out), nil
}

func parseLines(out string) []string {
	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, line)
	}
	return names
}

// LoadImageArchiveSpec describes one kind load image-archive call.
type LoadImageArchiveSpec struct {
	Name string

	// Path is a local tar file, as docker save writes one - kind load
	// image-archive takes a file path, not stdin.
	Path string
}

// LoadImageArchive runs kind load image-archive against spec, streaming its
// output to stderr as it runs.
func LoadImageArchive(ctx context.Context, spec LoadImageArchiveSpec, stderr io.Writer) error {
	args := []string{"load", "image-archive", spec.Path, "--name", spec.Name}
	if err := runStreamed(ctx, nil, io.Discard, stderr, nil, args...); err != nil {
		return fmt.Errorf("kindcmd: load image archive: %w", err)
	}
	return nil
}

// envWith returns the process's own environment plus extra, or nil - meaning
// the child inherits the process's environment unchanged - when extra is
// empty.
func envWith(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil
	}
	env := os.Environ()
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

// runBuffered calls the kind binary and returns its standard output, for a
// call whose result is data to parse rather than progress to show.
func runBuffered(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", notInstalled(fmt.Errorf("kind %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("kind %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing kind binary, so the raw exec.ErrNotFound chain doesn't reach the
// user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "kind isn't installed, or isn't on PATH - install it: https://kind.sigs.k8s.io/docs/user/quick-start/#installation")
}

// runStreamed calls the kind binary with stdout and stderr wired straight
// through to the caller's writers, for a call long enough that the caller
// needs to see progress as it happens rather than only once it exits. A nil
// env leaves the child's environment as the process's own; env otherwise
// replaces it outright (the caller builds it with [envWith]).
func runStreamed(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, env []string, args ...string) error {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if env != nil {
		cmd.Env = env
	}

	if err := cmd.Run(); err != nil {
		return notInstalled(fmt.Errorf("kind %s: %w", strings.Join(args, " "), err))
	}
	return nil
}
