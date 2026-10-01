// Package k3dcmd drives the k3d command line, on the host.
package k3dcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/uerr"
)

// Binary is the command that this package runs.
const Binary = "k3d"

// Available reports whether the k3d command runs.
func Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return fmt.Errorf("k3dcmd: %w: %w", ErrUnavailable, err)
	}
	if _, err := runBuffered(ctx, nil, "version"); err != nil {
		return fmt.Errorf("k3dcmd: %w: %w", ErrUnavailable, err)
	}
	return nil
}

// CreateSpec describes one k3d cluster create call.
type CreateSpec struct {
	Name string

	// Network names an existing docker network that every node joins.
	Network string

	// Image is the k3s image. Empty uses the default of the installed k3d.
	Image string

	// APIPort is the loopback host port for the Kubernetes API server. Zero
	// leaves k3d to publish it on every interface.
	APIPort int

	// NoRollback keeps the nodes of a cluster that fails to start.
	NoRollback bool

	// Memory limits the memory of every server and agent, such as "2g". Empty
	// leaves the nodes unlimited.
	Memory string

	// Agents is the worker count. The cluster always has one server.
	Agents int

	Wait time.Duration

	// Env names variables to set in every node's own environment, one --env
	// flag per entry.
	Env map[string]string

	// CommandEnv names extra variables (DOCKER_HOST, ...) set for the k3d
	// process only.
	CommandEnv map[string]string

	// Volumes names one "source:destination@nodefilter" entry per --volume
	// flag.
	Volumes []string

	// NodeLabels names one "key=value@nodefilter" entry per --k3s-node-label
	// flag.
	NodeLabels []string

	// K3sArgs names one "argument@nodefilter" entry per --k3s-arg flag.
	K3sArgs []string
}

// Create runs k3d cluster create against spec, streaming its output to
// stdout and stderr as it runs. It never touches the user's own kubeconfig.
func Create(ctx context.Context, spec CreateSpec, stdout, stderr io.Writer) error {
	if err := runStreamed(ctx, stdout, stderr, envWith(spec.CommandEnv), CreateArgs(spec)...); err != nil {
		return fmt.Errorf("k3dcmd: create cluster: %w", err)
	}
	return nil
}

// CreateArgs builds the argument list of the k3d cluster create call for
// spec.
func CreateArgs(spec CreateSpec) []string {
	args := []string{
		"cluster", "create", spec.Name,
		"--servers", "1",
		"--agents", strconv.Itoa(spec.Agents),
		"--network", spec.Network,
		"--kubeconfig-update-default=false",
		"--kubeconfig-switch-context=false",
		"--wait",
	}
	if spec.Wait > 0 {
		args = append(args, "--timeout", spec.Wait.String())
	}
	if spec.Image != "" {
		args = append(args, "--image", spec.Image)
	}
	if spec.APIPort > 0 {
		args = append(args, "--api-port", "127.0.0.1:"+strconv.Itoa(spec.APIPort))
	}
	if spec.NoRollback {
		args = append(args, "--no-rollback")
	}
	if spec.Memory != "" {
		args = append(args, "--servers-memory", spec.Memory, "--agents-memory", spec.Memory)
	}
	for _, key := range slices.Sorted(maps.Keys(spec.Env)) {
		args = append(args, "--env", key+"="+spec.Env[key]+"@all")
	}
	for _, volume := range spec.Volumes {
		args = append(args, "--volume", volume)
	}
	for _, label := range spec.NodeLabels {
		args = append(args, "--k3s-node-label", label)
	}
	for _, arg := range spec.K3sArgs {
		args = append(args, "--k3s-arg", arg)
	}
	return args
}

// Delete runs k3d cluster delete against name, streaming its output to
// stderr as it runs. A cluster that does not exist is not an error. env names
// extra variables (DOCKER_HOST, ...) set for the k3d process only.
func Delete(ctx context.Context, name string, env map[string]string, stderr io.Writer) error {
	nodes, err := GetNodes(ctx, name, env)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}
	if err := runStreamed(ctx, io.Discard, stderr, envWith(env), "cluster", "delete", name); err != nil {
		return fmt.Errorf("k3dcmd: delete cluster: %w", err)
	}
	return nil
}

// Roles of the nodes that GetNodes returns.
const (
	roleServer = "server"
	roleAgent  = "agent"
)

// GetNodes returns the container name of every server and agent node of
// cluster name, servers first, leaving out the load balancer. A cluster that
// does not exist reports (nil, nil), not an error. env names extra variables
// (DOCKER_HOST, ...) set for the k3d process only.
func GetNodes(ctx context.Context, name string, env map[string]string) ([]string, error) {
	out, err := runBuffered(ctx, envWith(env), "cluster", "list", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("k3dcmd: cluster list: %w", err)
	}
	return parseNodes(out, name)
}

func parseNodes(out, name string) ([]string, error) {
	var clusters []struct {
		Name  string `json:"name"`
		Nodes []struct {
			Name string `json:"name"`
			Role string `json:"role"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &clusters); err != nil {
		return nil, fmt.Errorf("k3dcmd: parse cluster list: %w", err)
	}
	var servers, agents []string
	for _, cluster := range clusters {
		if cluster.Name != name {
			continue
		}
		for _, node := range cluster.Nodes {
			switch node.Role {
			case roleServer:
				servers = append(servers, node.Name)
			case roleAgent:
				agents = append(agents, node.Name)
			}
		}
	}
	slices.Sort(servers)
	slices.Sort(agents)
	return append(servers, agents...), nil
}

// KubeconfigWrite runs k3d kubeconfig write, saving cluster name's
// kubeconfig to path and replacing whatever the file held. env names extra
// variables (DOCKER_HOST, ...) set for the k3d process only.
func KubeconfigWrite(ctx context.Context, name, path string, env map[string]string) error {
	if _, err := runBuffered(ctx, envWith(env), "kubeconfig", "write", name, "--output", path, "--overwrite"); err != nil {
		return fmt.Errorf("k3dcmd: write kubeconfig: %w", err)
	}
	return nil
}

// ImageImportSpec describes one k3d image import call.
type ImageImportSpec struct {
	Name string

	// Path is a local tar file, as docker save writes one.
	Path string

	// CommandEnv names extra variables (DOCKER_HOST, ...) set for the k3d
	// process only.
	CommandEnv map[string]string
}

// ImageImport runs k3d image import against spec, streaming its output to
// stderr as it runs.
func ImageImport(ctx context.Context, spec ImageImportSpec, stderr io.Writer) error {
	args := []string{"image", "import", spec.Path, "--cluster", spec.Name}
	if err := runStreamed(ctx, io.Discard, stderr, envWith(spec.CommandEnv), args...); err != nil {
		return fmt.Errorf("k3dcmd: image import: %w", err)
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

// runBuffered calls the k3d binary and returns its standard output, for a
// call whose result is data to parse rather than progress to show. A nil env
// leaves the child's environment as the process's own; env otherwise
// replaces it outright (the caller builds it with [envWith]).
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
			return "", notInstalled(fmt.Errorf("k3d %s: %w", strings.Join(args, " "), err))
		}
		return "", notInstalled(fmt.Errorf("k3d %s: %s: %w", strings.Join(args, " "), msg, err))
	}
	return stdout.String(), nil
}

// notInstalled attaches a human-facing message to err when it reports a
// missing k3d binary, so the raw exec.ErrNotFound chain doesn't reach the
// user unexplained. It returns err unchanged for any other failure.
func notInstalled(err error) error {
	if !errors.Is(err, exec.ErrNotFound) {
		return err
	}
	return uerr.Wrap(err, "k3d isn't installed, or isn't on PATH - install it: https://k3d.io/#installation")
}

// runStreamed calls the k3d binary with stdout and stderr wired straight
// through to the caller's writers, for a call long enough that the caller
// needs to see progress as it happens rather than only once it exits. A nil
// env leaves the child's environment as the process's own.
func runStreamed(ctx context.Context, stdout, stderr io.Writer, env []string, args ...string) error {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if env != nil {
		cmd.Env = env
	}

	if err := cmd.Run(); err != nil {
		return notInstalled(fmt.Errorf("k3d %s: %w", strings.Join(args, " "), err))
	}
	return nil
}
