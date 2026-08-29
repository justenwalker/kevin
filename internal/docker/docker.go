// Package docker implements [cri.Runtime] using docker.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

// Binary is the command that this package runs.
const Binary = "docker"

// Client runs docker commands. The zero value is ready to use; use [New]
// when the caller carries an engine_config blob.
type Client struct{}

var _ cri.Runtime = Client{}

// New builds a Client from the marshaled bytes of a [pb.DockerEngineConfig].
// Empty configBytes decodes to the zero message.
func New(configBytes []byte) (Client, error) {
	var cfg pb.DockerEngineConfig
	if len(configBytes) > 0 {
		if err := proto.Unmarshal(configBytes, &cfg); err != nil {
			return Client{}, fmt.Errorf("docker: decode engine config: %w", err)
		}
	}
	return Client{}, nil
}

// Available reports whether the docker command runs and the daemon answers.
func (Client) Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return uerr.Wrap(fmt.Errorf("docker: %w: %w", cri.ErrUnavailable, err),
			"docker isn't installed, or isn't on PATH")
	}
	if _, err := run(ctx, nil, "info", "--format", "{{.ServerVersion}}"); err != nil {
		return uerr.Wrap(fmt.Errorf("docker: the daemon does not answer: %w", cri.ErrUnavailable),
			"Docker isn't running - start Docker Desktop (or dockerd), then retry")
	}
	return nil
}

// NetworkCreate implements [cri.Runtime] for docker.
func (Client) NetworkCreate(ctx context.Context, name string, labels map[string]string) error {
	if ok, err := networkExists(ctx, name); err != nil {
		return err
	} else if ok {
		return nil
	}

	labels2 := labelArgs(labels)
	args := make([]string, 0, len(labels2)+3)
	args = append(args, "network", "create")
	args = append(args, labels2...)
	args = append(args, name)

	if _, err := run(ctx, nil, args...); err != nil {
		// Another caller can create the network between the check above and
		// this call. Check again before the error is reported.
		if ok, existsErr := networkExists(ctx, name); existsErr == nil && ok {
			return nil
		}
		return fmt.Errorf("docker: create network %q: %w", name, err)
	}
	return nil
}

// NetworkRemove implements [cri.Runtime] for docker. A network that still
// carries a live container is left in place rather than treated as an error.
func (Client) NetworkRemove(ctx context.Context, name string) error {
	if _, err := run(ctx, nil, "network", "rm", name); err != nil {
		// docker's error text for a missing network, or one still in use, is
		// not a stable API across versions. Ask docker directly instead of
		// guessing from the message.
		if ok, existsErr := networkExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		if inUse, inUseErr := networkInUse(ctx, name); inUseErr == nil && inUse {
			return nil
		}
		return fmt.Errorf("docker: remove network %q: %w", name, err)
	}
	return nil
}

// networkInUse reports whether name still carries any attached container.
func networkInUse(ctx context.Context, name string) (bool, error) {
	out, err := run(ctx, nil, "network", "inspect", name, "--format", "{{len .Containers}}")
	if err != nil {
		return false, fmt.Errorf("docker: inspect network %q: %w", name, err)
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return false, fmt.Errorf("docker: parse network %q container count: %w", name, convErr)
	}
	return count > 0, nil
}

// networkExists asks docker for the network by exact name, rather than
// inferring absence from the wording of an error message.
func networkExists(ctx context.Context, name string) (bool, error) {
	out, err := run(ctx, nil, "network", "ls", "--format", "{{.Name}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("docker: list networks: %w", err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// NetworkGateway returns the IPv4 gateway address of a network.
// NetworkGateway returns [cri.ErrNotFound] when the network does not exist,
// and [cri.ErrNoGateway] when the network carries no IPv4 gateway.
func (Client) NetworkGateway(ctx context.Context, name string) (netip.Addr, error) {
	out, err := run(ctx, nil, "network", "inspect", name,
		"--format", "{{range .IPAM.Config}}{{.Gateway}} {{end}}")
	if err != nil {
		if ok, existsErr := networkExists(ctx, name); existsErr == nil && !ok {
			return netip.Addr{}, fmt.Errorf("docker: inspect network %q: %w", name, cri.ErrNotFound)
		}
		return netip.Addr{}, fmt.Errorf("docker: inspect network %q: %w", name, err)
	}

	gateway, err := gatewayFromInspect(out)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("docker: inspect network %q: %w", name, err)
	}
	return gateway, nil
}

// gatewayFromInspect finds the first IPv4 address in the space-separated
// list that a gateway template produces.
func gatewayFromInspect(out string) (netip.Addr, error) {
	for field := range strings.FieldsSeq(out) {
		addr, err := netip.ParseAddr(field)
		if err != nil {
			continue
		}
		if addr.Is4() {
			return addr, nil
		}
	}
	return netip.Addr{}, cri.ErrNoGateway
}

// NetworkConnect joins a container to a network. A container that is on the network already is not an error.
func (Client) NetworkConnect(ctx context.Context, network, container string) error {
	if _, err := run(ctx, nil, "network", "connect", network, container); err != nil {
		if ok, checkErr := containerOnNetwork(ctx, container, network); checkErr == nil && ok {
			return nil
		}
		return fmt.Errorf("docker: connect %q to %q: %w", container, network, err)
	}
	return nil
}

// containerOnNetwork reports if the container is already connected to the network.
func containerOnNetwork(ctx context.Context, container, network string) (bool, error) {
	out, err := run(ctx, nil, "inspect", "--type", "container",
		"--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return false, fmt.Errorf("docker: inspect %q: %w", container, err)
	}

	var networks map[string]json.RawMessage
	if jsonErr := json.Unmarshal([]byte(out), &networks); jsonErr != nil {
		return false, fmt.Errorf("docker: inspect %q: decode: %w", container, jsonErr)
	}
	_, ok := networks[network]
	return ok, nil
}

// runArgs builds the docker arguments for a spec. The arguments are stable
// across calls for one spec.
func runArgs(spec cri.RunSpec) []string {
	args := []string{"run", "--detach", "--name", spec.Name}

	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	if spec.Alias != "" {
		args = append(args, "--network-alias", spec.Alias)
	}
	if spec.Pull {
		args = append(args, "--pull", "always")
	}
	if len(spec.Entrypoint) > 0 {
		args = append(args, "--entrypoint", spec.Entrypoint[0])
	}

	args = append(args, labelArgs(spec.Labels)...)

	// Sort the keys. A map has no order, and an unstable command line turns
	// every diff of a log into noise.
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	for _, p := range spec.Ports {
		args = append(args, "--publish", p)
	}
	for _, v := range spec.Volumes {
		args = append(args, "--volume", v)
	}
	for _, d := range spec.DNS {
		args = append(args, "--dns", d)
	}
	for _, h := range spec.AddHosts {
		args = append(args, "--add-host", h)
	}

	args = append(args, spec.Image)
	if len(spec.Entrypoint) > 1 {
		args = append(args, spec.Entrypoint[1:]...)
	}
	return append(args, spec.Cmd...)
}

func labelArgs(labels map[string]string) []string {
	args := make([]string, 0, len(labels)*2)
	for _, k := range sortedKeys(labels) {
		args = append(args, "--label", k+"="+labels[k])
	}
	return args
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Run implements [cri.Runtime] for docker.
func (Client) Run(ctx context.Context, spec cri.RunSpec) (string, error) {
	out, err := run(ctx, nil, runArgs(spec)...)
	if err != nil {
		return "", friendlyRunErr(fmt.Errorf("docker: run %q: %w", spec.Name, err), spec)
	}
	return strings.TrimSpace(out), nil
}

// friendlyRunErr attaches a human-facing message to err when its text names
// one of the docker run failures users hit most often - a port already in
// use, or an image that couldn't be found or pulled. It returns err
// unchanged for anything else: guessing at an unfamiliar docker error is
// worse than showing its raw text.
func friendlyRunErr(err error, spec cri.RunSpec) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "port is already allocated") || strings.Contains(msg, "address already in use"):
		return uerr.Wrap(err, "a port %s needs is already in use on this machine - stop whatever is using it, or change the step's published ports", spec.Name)
	case strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "pull access denied") || strings.Contains(msg, "repository does not exist"):
		return uerr.Wrap(err, "the image %q couldn't be found or pulled - check the name and tag, and that you're logged in if it's private", spec.Image)
	default:
		return err
	}
}

// Remove implements [cri.Runtime] for docker.
func (Client) Remove(ctx context.Context, name string) error {
	if _, err := run(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
		if ok, existsErr := containerExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		return fmt.Errorf("docker: remove %q: %w", name, err)
	}
	return nil
}

// containerExists asks docker for the container by exact name, rather than
// inferring absence from the wording of an error message.
func containerExists(ctx context.Context, name string) (bool, error) {
	out, err := run(ctx, nil, "ps", "--all", "--format", "{{.Names}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("docker: list containers: %w", err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// inspectResult mirrors the fields of docker inspect that kevin reads. A
// missing field decodes as a zero value.
type inspectResult struct {
	ID    string
	Name  string
	State struct {
		Running  bool
		ExitCode int
	}
	Config struct {
		Labels map[string]string
	}
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string
		}
		Ports map[string][]struct {
			HostIP   string
			HostPort string
		}
	}
}

// Inspect implements [cri.Runtime] for docker.
func (Client) Inspect(ctx context.Context, name string) (cri.Container, error) {
	out, err := run(ctx, nil, "inspect", "--type", "container", "--format", "{{json .}}", name)
	if err != nil {
		if ok, existsErr := containerExists(ctx, name); existsErr == nil && !ok {
			return cri.Container{}, fmt.Errorf("docker: inspect %q: %w", name, cri.ErrNotFound)
		}
		return cri.Container{}, fmt.Errorf("docker: inspect %q: %w", name, err)
	}

	var raw inspectResult
	if jsonErr := json.Unmarshal([]byte(out), &raw); jsonErr != nil {
		return cri.Container{}, fmt.Errorf("docker: inspect %q: decode: %w", name, jsonErr)
	}

	return fromInspect(raw), nil
}

func fromInspect(raw inspectResult) cri.Container {
	c := cri.Container{
		ID:       raw.ID,
		Name:     strings.TrimPrefix(raw.Name, "/"),
		Running:  raw.State.Running,
		ExitCode: raw.State.ExitCode,
		IPs:      map[string]string{},
		Ports:    map[string]string{},
		Labels:   raw.Config.Labels,
	}
	for network, settings := range raw.NetworkSettings.Networks {
		if settings.IPAddress != "" {
			c.IPs[network] = settings.IPAddress
		}
	}
	for port, bindings := range raw.NetworkSettings.Ports {
		if len(bindings) == 0 {
			continue
		}
		host := bindings[0].HostIP
		if host == "" || host == "0.0.0.0" {
			host = "127.0.0.1"
		}
		c.Ports[port] = host + ":" + bindings[0].HostPort
	}
	return c
}

// ListByLabel returns the names of the containers that carry a label.
func (Client) ListByLabel(ctx context.Context, key, value string) ([]string, error) {
	// No --quiet here. The flag overrides --format, and the output becomes a
	// list of IDs.
	out, err := run(ctx, nil, "ps", "--all", "--no-trunc",
		"--format", "{{.Names}}", "--filter", "label="+key+"="+value)
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Exec runs a command inside a container and returns its standard output.
func (c Client) Exec(ctx context.Context, container string, args ...string) (string, error) {
	return c.ExecInput(ctx, container, nil, args...)
}

// ExecInput runs a command inside a container, with stdin feeding the
// command, and returns its standard output.
func (Client) ExecInput(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error) {
	full := make([]string, 0, len(args)+3)
	full = append(full, "exec")
	if stdin != nil {
		full = append(full, "-i")
	}
	full = append(full, container)
	full = append(full, args...)

	out, err := run(ctx, stdin, full...)
	if err != nil {
		if ok, existsErr := containerExists(ctx, container); existsErr == nil && !ok {
			return "", fmt.Errorf("docker: exec %q: %w", container, cri.ErrNotFound)
		}
		return "", fmt.Errorf("docker: exec %q: %w", container, err)
	}
	return out, nil
}

// Save streams a docker image as a tar archive - the format
// nodeutils.LoadImageArchive expects. The caller must close the returned
// reader; Close waits for the docker process to exit.
//
// This bypasses run/Exec deliberately: those buffer the whole output as a
// string, and an image archive can be hundreds of megabytes.
func (Client) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "docker", "save", image) //nolint:gosec // image is a locally-built tag, not user input
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("docker: save %s: %w", image, err)
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("docker: save %s: %w", image, err)
	}
	return &saveReader{ReadCloser: stdout, cmd: cmd}, nil
}

// saveReader waits for the docker save process to exit when the caller
// closes the stream, so the process is never left behind.
type saveReader struct {
	io.ReadCloser

	cmd *exec.Cmd
}

func (r *saveReader) Close() error {
	_ = r.ReadCloser.Close()
	return r.cmd.Wait() //nolint:wrapcheck // Close implements io.Closer; the stdlib convention returns the raw error
}

// run calls the docker binary and returns the standard output.
// A nil stdin gives the command no standard input.
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
			return "", fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("docker %s: %s: %w", strings.Join(args, " "), msg, err)
	}
	return stdout.String(), nil
}
