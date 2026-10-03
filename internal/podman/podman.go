// Package podman implements [cri.Runtime] using podman.
package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/uerr"
	"github.com/justenwalker/kevin/protos/pb"
)

// Binary is the command that this package runs.
const Binary = "podman"

// Client runs podman commands. The zero value is ready to use; use [New]
// when the caller carries an engine_config blob.
type Client struct {
	// Runner runs the binary. Nil runs real processes with [command.Default].
	Runner command.Runner
}

var _ cri.Runtime = Client{}

// New builds a Client from the marshaled bytes of a [pb.PodmanEngineConfig].
// Empty configBytes decodes to the zero message.
func New(configBytes []byte) (Client, error) {
	var cfg pb.PodmanEngineConfig
	if len(configBytes) > 0 {
		if err := proto.Unmarshal(configBytes, &cfg); err != nil {
			return Client{}, fmt.Errorf("podman: decode engine config: %w", err)
		}
	}
	return Client{}, nil
}

// Available reports whether the podman command runs and answers.
func (c Client) Available(ctx context.Context) error {
	if _, err := exec.LookPath(Binary); err != nil {
		return uerr.Wrap(fmt.Errorf("podman: %w: %w", cri.ErrUnavailable, err),
			"podman isn't installed, or isn't on PATH")
	}
	if _, err := c.run(ctx, nil, "info", "--format", "{{.Version.Version}}"); err != nil {
		return uerr.Wrap(fmt.Errorf("podman: the daemon does not answer: %w", cri.ErrUnavailable),
			"podman isn't running - start it (podman machine start on macOS), then retry")
	}
	return nil
}

// Socket returns the path of the Docker-compatible API socket of the podman
// service. It asks podman info on Linux and the default podman machine
// elsewhere, and returns [ErrNoSocket] when podman names none.
func (c Client) Socket(ctx context.Context) (string, error) {
	args := []string{"info", "--format", "{{.Host.RemoteSocket.Path}}"}
	if runtime.GOOS != "linux" {
		args = []string{"machine", "inspect", "--format", "{{.ConnectionInfo.PodmanSocket.Path}}"}
	}
	out, err := c.run(ctx, nil, args...)
	if err != nil {
		return "", fmt.Errorf("podman: find the API socket: %w", err)
	}
	return socketPath(out)
}

// socketPath extracts the socket path from the output of the podman command
// that Socket runs.
func socketPath(out string) (string, error) {
	path := strings.TrimPrefix(strings.TrimSpace(out), "unix://")
	if path == "" || path == "<no value>" {
		return "", ErrNoSocket
	}
	return path, nil
}

// NetworkCreate implements [cri.Runtime] for podman.
func (c Client) NetworkCreate(ctx context.Context, name string, opts cri.NetworkOptions) error {
	if ok, err := c.networkExists(ctx, name); err != nil {
		return err
	} else if ok {
		return nil
	}

	labels2 := labelArgs(opts.Labels)
	args := make([]string, 0, len(labels2)+4)
	args = append(args, "network", "create")
	if opts.IPv6 {
		args = append(args, "--ipv6")
	}
	args = append(args, labels2...)
	args = append(args, name)

	if _, err := c.run(ctx, nil, args...); err != nil {
		// Another caller can create the network between the check above and
		// this call. Check again before the error is reported.
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && ok {
			return nil
		}
		return fmt.Errorf("podman: create network %q: %w", name, err)
	}
	return nil
}

// NetworkRemove implements [cri.Runtime] for podman. A network that still
// carries a live container is left in place rather than treated as an error.
func (c Client) NetworkRemove(ctx context.Context, name string) error {
	if _, err := c.run(ctx, nil, "network", "rm", name); err != nil {
		// podman's error text for a missing network, or one still in use, is
		// not a stable API across versions. Ask podman directly instead of
		// guessing from the message.
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		if inUse, inUseErr := c.networkInUse(ctx, name); inUseErr == nil && inUse {
			return nil
		}
		return fmt.Errorf("podman: remove network %q: %w", name, err)
	}
	return nil
}

// networkInUse reports whether name still carries any attached container.
func (c Client) networkInUse(ctx context.Context, name string) (bool, error) {
	out, err := c.run(ctx, nil, "network", "inspect", name, "--format", "{{len .Containers}}")
	if err != nil {
		return false, fmt.Errorf("podman: inspect network %q: %w", name, err)
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return false, fmt.Errorf("podman: parse network %q container count: %w", name, convErr)
	}
	return count > 0, nil
}

// networkExists asks podman for the network by exact name, rather than
// inferring absence from the wording of an error message.
func (c Client) networkExists(ctx context.Context, name string) (bool, error) {
	out, err := c.run(ctx, nil, "network", "ls", "--format", "{{.Name}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("podman: list networks: %w", err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// NetworkGateway returns the gateway addresses of a network.
// NetworkGateway returns [cri.ErrNotFound] when the network does not exist,
// and [cri.ErrNoGateway] when the network carries no gateway in either
// address family.
func (c Client) NetworkGateway(ctx context.Context, name string) (cri.Gateway, error) {
	out, err := c.run(ctx, nil, "network", "inspect", name,
		"--format", "{{range .Subnets}}{{.Gateway}} {{end}}")
	if err != nil {
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && !ok {
			return cri.Gateway{}, fmt.Errorf("podman: inspect network %q: %w", name, cri.ErrNotFound)
		}
		return cri.Gateway{}, fmt.Errorf("podman: inspect network %q: %w", name, err)
	}

	gateway, err := gatewayFromInspect(out)
	if err != nil {
		return cri.Gateway{}, fmt.Errorf("podman: inspect network %q: %w", name, err)
	}
	return gateway, nil
}

// gatewayFromInspect finds the first IPv4 and first IPv6 address in the
// space-separated list that a gateway template produces.
func gatewayFromInspect(out string) (cri.Gateway, error) {
	var gw cri.Gateway
	for field := range strings.FieldsSeq(out) {
		addr, err := netip.ParseAddr(field)
		if err != nil {
			continue
		}
		if addr.Is4() {
			if !gw.V4.IsValid() {
				gw.V4 = addr
			}
			continue
		}
		if !gw.V6.IsValid() {
			gw.V6 = addr
		}
	}
	if !gw.V4.IsValid() && !gw.V6.IsValid() {
		return cri.Gateway{}, cri.ErrNoGateway
	}
	return gw, nil
}

// NetworkConnect joins a container to a network, and points the default route
// of the container at the gateway of that network with ip inside the
// container, which must be privileged and carry iproute2. A container that is
// on the network already is not an error.
func (c Client) NetworkConnect(ctx context.Context, network, container string) error {
	if _, err := c.run(ctx, nil, "network", "connect", network, container); err != nil {
		if ok, checkErr := c.containerOnNetwork(ctx, container, network); checkErr != nil || !ok {
			return fmt.Errorf("podman: connect %q to %q: %w", container, network, err)
		}
	}

	gw, err := c.NetworkGateway(ctx, network)
	if err != nil {
		return fmt.Errorf("podman: gateway of %q: %w", network, err)
	}
	route := defaultRouteArgs(gw)
	if route == nil {
		return nil // an IPv6-only network has no IPv4 default route to set
	}
	if _, err = c.Exec(ctx, container, route...); err != nil {
		return fmt.Errorf("podman: route %q through %q: %w", container, network, err)
	}
	return nil
}

// defaultRouteArgs builds the ip command that points the default route of a
// container at gw. It returns nil when gw has no IPv4 address.
func defaultRouteArgs(gw cri.Gateway) []string {
	if !gw.V4.IsValid() {
		return nil
	}
	return []string{"ip", "route", "replace", "default", "via", gw.V4.String()}
}

// containerOnNetwork reports if the container is already connected to the network.
func (c Client) containerOnNetwork(ctx context.Context, container, network string) (bool, error) {
	out, err := c.run(ctx, nil, "inspect", "--type", "container",
		"--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return false, fmt.Errorf("podman: inspect %q: %w", container, err)
	}

	var networks map[string]json.RawMessage
	if jsonErr := json.Unmarshal([]byte(out), &networks); jsonErr != nil {
		return false, fmt.Errorf("podman: inspect %q: decode: %w", container, jsonErr)
	}
	_, ok := networks[network]
	return ok, nil
}

// runArgs builds the podman arguments for a spec. The arguments are stable
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
	args = append(args, processArgs(spec)...)

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
	for _, c := range spec.CapAdd {
		args = append(args, "--cap-add", c)
	}
	if spec.PidHost {
		args = append(args, "--pid", "host")
	}

	args = append(args, spec.Image)
	if len(spec.Entrypoint) > 1 {
		args = append(args, spec.Entrypoint[1:]...)
	}
	return append(args, spec.Cmd...)
}

// processArgs builds the flags for the user, working directory, and limits.
func processArgs(spec cri.RunSpec) []string {
	var args []string
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if spec.Workdir != "" {
		args = append(args, "--workdir", spec.Workdir)
	}
	if spec.CPUs != "" {
		args = append(args, "--cpus", spec.CPUs)
	}
	if spec.Memory != "" {
		args = append(args, "--memory", spec.Memory)
	}
	return args
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

// buildArgs builds the podman arguments for an image build.
func buildArgs(spec cri.BuildSpec) []string {
	args := []string{"build", "--tag", spec.Tag}
	if spec.Dockerfile != "" {
		args = append(args, "--file", spec.Dockerfile)
	}
	if spec.Target != "" {
		args = append(args, "--target", spec.Target)
	}
	args = append(args, labelArgs(spec.Labels)...)
	for _, k := range sortedKeys(spec.Args) {
		args = append(args, "--build-arg", k+"="+spec.Args[k])
	}
	return append(args, spec.Context)
}

// Build implements [cri.Runtime] for podman. The output of podman build, on
// both streams, goes to out as it arrives.
func (c Client) Build(ctx context.Context, spec cri.BuildSpec, out io.Writer) error {
	args := buildArgs(spec)
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := c.runCmd(ctx, cmd); err != nil {
		return fmt.Errorf("podman: build %q: %w", spec.Tag, err)
	}
	return nil
}

// Run implements [cri.Runtime] for podman.
func (c Client) Run(ctx context.Context, spec cri.RunSpec) (string, error) {
	out, err := c.run(ctx, nil, runArgs(spec)...)
	if err != nil {
		return "", friendlyRunErr(fmt.Errorf("podman: run %q: %w", spec.Name, err), spec)
	}
	return strings.TrimSpace(out), nil
}

// friendlyRunErr attaches a human-facing message to err when its text names
// one of the podman run failures users hit most often - a port already in
// use, or an image that couldn't be found or pulled. It returns err
// unchanged for anything else: guessing at an unfamiliar podman error is
// worse than showing its raw text.
func friendlyRunErr(err error, spec cri.RunSpec) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated"):
		return uerr.Wrap(err, "a port %s needs is already in use on this machine - stop whatever is using it, or change the step's published ports", spec.Name)
	case strings.Contains(msg, "manifest unknown") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "repository does not exist") || strings.Contains(msg, "image not known"):
		return uerr.Wrap(err, "the image %q couldn't be found or pulled - check the name and tag, and that you're logged in if it's private", spec.Image)
	default:
		return err
	}
}

// Remove implements [cri.Runtime] for podman.
func (c Client) Remove(ctx context.Context, name string) error {
	if _, err := c.run(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
		if ok, existsErr := c.containerExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		return fmt.Errorf("podman: remove %q: %w", name, err)
	}
	return nil
}

// containerExists asks podman for the container by exact name, rather than
// inferring absence from the wording of an error message.
func (c Client) containerExists(ctx context.Context, name string) (bool, error) {
	out, err := c.run(ctx, nil, "ps", "--all", "--format", "{{.Names}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("podman: list containers: %w", err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// inspectResult mirrors the fields of podman inspect that kevin reads. A
// missing field decodes as a zero value.
type inspectResult struct {
	ID    string
	Name  string
	State struct {
		Running  bool
		Status   string
		ExitCode int
		Pid      int
	}
	Config struct {
		Labels map[string]string
	}
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress         string
			GlobalIPv6Address string
		}
		Ports map[string][]struct {
			HostIP   string
			HostPort string
		}
	}
}

// Inspect implements [cri.Runtime] for podman.
func (c Client) Inspect(ctx context.Context, name string) (cri.Container, error) {
	out, err := c.run(ctx, nil, "inspect", "--type", "container", "--format", "{{json .}}", name)
	if err != nil {
		if ok, existsErr := c.containerExists(ctx, name); existsErr == nil && !ok {
			return cri.Container{}, fmt.Errorf("podman: inspect %q: %w", name, cri.ErrNotFound)
		}
		return cri.Container{}, fmt.Errorf("podman: inspect %q: %w", name, err)
	}

	var raw inspectResult
	if jsonErr := json.Unmarshal([]byte(out), &raw); jsonErr != nil {
		return cri.Container{}, fmt.Errorf("podman: inspect %q: decode: %w", name, jsonErr)
	}

	return fromInspect(raw), nil
}

func fromInspect(raw inspectResult) cri.Container {
	c := cri.Container{
		ID:       raw.ID,
		Name:     strings.TrimPrefix(raw.Name, "/"),
		Running:  raw.State.Running,
		Exited:   cri.StatusExited(raw.State.Status),
		ExitCode: raw.State.ExitCode,
		IPs:      map[string]string{},
		IPv6:     map[string]string{},
		Ports:    map[string]string{},
		Labels:   raw.Config.Labels,
	}
	if raw.State.Pid != 0 {
		c.NetnsPath = fmt.Sprintf("/proc/%d/ns/net", raw.State.Pid)
	}
	for network, settings := range raw.NetworkSettings.Networks {
		if settings.IPAddress != "" {
			c.IPs[network] = settings.IPAddress
		}
		if settings.GlobalIPv6Address != "" {
			c.IPv6[network] = settings.GlobalIPv6Address
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
func (c Client) ListByLabel(ctx context.Context, key, value string) ([]string, error) {
	// No --quiet here. The flag overrides --format, and the output becomes a
	// list of IDs.
	out, err := c.run(ctx, nil, "ps", "--all", "--no-trunc",
		"--format", "{{.Names}}", "--filter", "label="+key+"="+value)
	if err != nil {
		return nil, fmt.Errorf("podman: list containers: %w", err)
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
func (c Client) ExecInput(ctx context.Context, container string, stdin io.Reader, args ...string) (string, error) {
	full := make([]string, 0, len(args)+3)
	full = append(full, "exec")
	if stdin != nil {
		full = append(full, "-i")
	}
	full = append(full, container)
	full = append(full, args...)

	out, err := c.run(ctx, stdin, full...)
	if err != nil {
		if ok, existsErr := c.containerExists(ctx, container); existsErr == nil && !ok {
			return "", fmt.Errorf("podman: exec %q: %w", container, cri.ErrNotFound)
		}
		return "", fmt.Errorf("podman: exec %q: %w", container, err)
	}
	return out, nil
}

// Save streams a podman image as a tar archive - the format
// nodeutils.LoadImageArchive expects. The caller must close the returned
// reader; Close waits for the podman process to exit.
//
// This bypasses run/Exec deliberately: those buffer the whole output as a
// string, and an image archive can be hundreds of megabytes.
func (c Client) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	//nolint:gosec // image is a locally-built tag, not user input
	cmd := exec.CommandContext(ctx, Binary, "save", image)
	pr, pw := io.Pipe()
	cmd.Stdout = pw

	done := make(chan error, 1)
	go func() {
		err := c.runCmd(ctx, cmd)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	return &saveReader{ReadCloser: pr, cancel: cancel, done: done}, nil
}

// saveReader stops the podman save process and waits for it to exit when the
// caller closes the stream, so the process is never left behind.
type saveReader struct {
	io.ReadCloser

	cancel context.CancelFunc
	done   <-chan error
}

func (r *saveReader) Close() error {
	_ = r.ReadCloser.Close()
	r.cancel()
	return <-r.done
}

// run calls the podman binary and returns the standard output.
// A nil stdin gives the command no standard input.
func (c Client) run(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, Binary, args...)
	cmd.Stdin = stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := c.runCmd(ctx, cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("podman %s: %s: %w", strings.Join(args, " "), msg, err)
	}
	return stdout.String(), nil
}

// runCmd runs cmd with the Runner of c, or with command.Default when c has
// none.
func (c Client) runCmd(ctx context.Context, cmd *exec.Cmd) error {
	if c.Runner == nil {
		return command.Run(ctx, cmd)
	}
	return c.Runner.Run(ctx, cmd)
}
