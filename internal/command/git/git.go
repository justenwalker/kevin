// Package git drives the git command line, on the host.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "git"

// Client runs git through a [command.Runner]. It is safe for concurrent use
// when its runner is.
type Client struct {
	runner command.Runner
}

// New returns a Client that runs git with runner.
func New(runner command.Runner) *Client {
	return &Client{runner: runner}
}

// Available reports whether the git command runs.
func (c *Client) Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("git: %w: %w", ErrUnavailable, err)
	}
	if _, err := c.run(ctx, "version"); err != nil {
		return fmt.Errorf("git: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// CloneSpec describes one git clone call.
type CloneSpec struct {
	URL string

	// Dir is the target directory. It must not already exist - Clone
	// never merges into or overwrites one.
	Dir string

	// Env adds environment variables to the git process, layered onto the
	// process's own environment - never the process's own environment
	// itself.
	Env map[string]string
}

// Clone runs git clone against spec. Dir must not already exist; the
// caller owns any temp-dir-then-atomic-rename orchestration.
func (c *Client) Clone(ctx context.Context, spec CloneSpec) error {
	//nolint:gosec // every argument comes from caller-supplied index source configuration
	cmd := exec.CommandContext(ctx, Binary, "clone", spec.URL, spec.Dir)
	if env := envWith(spec.Env); env != nil {
		cmd.Env = env
	}

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := c.runner.Run(ctx, cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return notInstalled(fmt.Errorf("git clone %s: %w", spec.URL, err))
		}
		return notInstalled(fmt.Errorf("git clone %s: %s: %w", spec.URL, msg, err))
	}
	return nil
}

// envWith returns the process's own environment plus extra, or nil -
// meaning the child inherits the process's environment unchanged - when
// extra is empty.
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

// run calls the git binary and returns its standard output.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	//nolint:gosec // every argument is a fixed literal passed by this package's own callers
	cmd := exec.CommandContext(ctx, Binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := c.runner.Run(ctx, cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", notInstalled(fmt.Errorf("git %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing git binary, so the raw exec.ErrNotFound chain doesn't reach the
// user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "git isn't installed, or isn't on PATH - install it: https://git-scm.com/downloads")
}
