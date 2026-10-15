// Package clirt implements [container.Runtime] by shelling out to a docker-compatible
// container engine CLI. The docker and podman packages differ only in the
// [Dialect] they supply.
package clirt

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

	"github.com/justenwalker/kevin/internal/command"
	"github.com/justenwalker/kevin/internal/container"
	"github.com/justenwalker/kevin/internal/uerr"
)

// Dialect is what differs between the docker-compatible engines.
type Dialect struct {
	// Binary is the command to run. It also labels errors.
	Binary string

	// VersionTemplate is the "info --format" template that prints a value only
	// when the daemon answers.
	VersionTemplate string

	// GatewayTemplate is the "network inspect --format" template that prints
	// the space-separated gateway addresses of a network.
	GatewayTemplate string

	// DisableIPv6 passes --ipv6=false to "network create" when IPv6 is off,
	// for a daemon that can default to dual-stack networks.
	DisableIPv6 bool

	// NotInstalledHint and NotRunningHint are the messages shown when the
	// binary is missing, and when it runs but the engine does not answer.
	NotInstalledHint string
	NotRunningHint   string

	// PortInUse and ImageMissing are substrings of the stderr of a failed run
	// that name a port that is already taken, and an image that could not be
	// found or pulled.
	PortInUse    []string
	ImageMissing []string
}

// Flavor supplies the Dialect of a Client. A Flavor is a stateless type, so
// the zero value of a Client is ready to use.
type Flavor interface {
	Dialect() Dialect
}

// Client runs the CLI of a docker-compatible engine.
type Client[F Flavor] struct {
	// Runner runs the binary. Nil runs real processes with [command.Default].
	Runner command.Runner
}

// dialect returns the Dialect of c.
func (Client[F]) dialect() Dialect {
	var f F
	return f.Dialect()
}

// Available reports whether the engine command runs and its daemon answers.
func (c Client[F]) Available(ctx context.Context) error {
	d := c.dialect()
	if _, err := exec.LookPath(d.Binary); err != nil {
		return uerr.Wrap(fmt.Errorf("%s: %w: %w", d.Binary, container.ErrUnavailable, err), "%s", d.NotInstalledHint)
	}
	if _, err := c.Output(ctx, nil, "info", "--format", d.VersionTemplate); err != nil {
		return uerr.Wrap(fmt.Errorf("%s: the daemon does not answer: %w", d.Binary, container.ErrUnavailable), "%s", d.NotRunningHint)
	}
	return nil
}

// NetworkCreate implements [container.Runtime] for the engine.
func (c Client[F]) NetworkCreate(ctx context.Context, name string, opts container.NetworkOptions) error {
	if ok, err := c.networkExists(ctx, name); err != nil {
		return err
	} else if ok {
		return nil
	}

	if _, err := c.Output(ctx, nil, networkCreateArgs(c.dialect(), name, opts)...); err != nil {
		// Another caller can create the network between the check above and
		// this call. Check again before the error is reported.
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && ok {
			return nil
		}
		return fmt.Errorf("%s: create network %q: %w", c.dialect().Binary, name, err)
	}
	return nil
}

// networkCreateArgs builds the engine arguments that create the network name.
func networkCreateArgs(d Dialect, name string, opts container.NetworkOptions) []string {
	labels := labelArgs(opts.Labels)
	args := make([]string, 0, len(labels)+4)
	args = append(args, "network", "create")
	if opts.IPv6 {
		// Docker >= 26 auto-assigns a ULA subnet for --ipv6 with no
		// --subnet given. An older daemon needs an explicit subnet or this
		// fails - surfaced as a plain engine error, not guessed at.
		args = append(args, "--ipv6")
	} else if d.DisableIPv6 {
		// A daemon can default to dual-stack networks, which a cluster tool
		// may not handle.
		args = append(args, "--ipv6=false")
	}
	args = append(args, labels...)
	return append(args, name)
}

// NetworkRemove implements [container.Runtime] for the engine. A network that still
// carries a live container is left in place rather than treated as an error.
func (c Client[F]) NetworkRemove(ctx context.Context, name string) error {
	if _, err := c.Output(ctx, nil, "network", "rm", name); err != nil {
		// the engine's error text for a missing network, or one still in use, is
		// not a stable API across versions. Ask the engine directly instead of
		// guessing from the message.
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		if inUse, inUseErr := c.networkInUse(ctx, name); inUseErr == nil && inUse {
			return nil
		}
		return fmt.Errorf("%s: remove network %q: %w", c.dialect().Binary, name, err)
	}
	return nil
}

// networkInUse reports whether name still carries any attached container.
func (c Client[F]) networkInUse(ctx context.Context, name string) (bool, error) {
	out, err := c.Output(ctx, nil, "network", "inspect", name, "--format", "{{len .Containers}}")
	if err != nil {
		return false, fmt.Errorf("%s: inspect network %q: %w", c.dialect().Binary, name, err)
	}
	count, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return false, fmt.Errorf("%s: parse network %q container count: %w", c.dialect().Binary, name, convErr)
	}
	return count > 0, nil
}

// networkExists asks the engine for the network by exact name, rather than
// inferring absence from the wording of an error message.
func (c Client[F]) networkExists(ctx context.Context, name string) (bool, error) {
	out, err := c.Output(ctx, nil, "network", "ls", "--format", "{{.Name}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("%s: list networks: %w", c.dialect().Binary, err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// NetworkGateway returns the gateway addresses of a network.
// NetworkGateway returns [container.ErrNotFound] when the network does not exist,
// and [container.ErrNoGateway] when the network carries no gateway in either
// address family.
func (c Client[F]) NetworkGateway(ctx context.Context, name string) (container.Gateway, error) {
	out, err := c.Output(ctx, nil, "network", "inspect", name,
		"--format", c.dialect().GatewayTemplate)
	if err != nil {
		if ok, existsErr := c.networkExists(ctx, name); existsErr == nil && !ok {
			return container.Gateway{}, fmt.Errorf("%s: inspect network %q: %w", c.dialect().Binary, name, container.ErrNotFound)
		}
		return container.Gateway{}, fmt.Errorf("%s: inspect network %q: %w", c.dialect().Binary, name, err)
	}

	gateway, err := gatewayFromInspect(out)
	if err != nil {
		return container.Gateway{}, fmt.Errorf("%s: inspect network %q: %w", c.dialect().Binary, name, err)
	}
	return gateway, nil
}

// gatewayFromInspect finds the first IPv4 and first IPv6 address in the
// space-separated list that a gateway template produces.
func gatewayFromInspect(out string) (container.Gateway, error) {
	var gw container.Gateway
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
		return container.Gateway{}, container.ErrNoGateway
	}
	return gw, nil
}

// NetworkConnect joins a container to a network, and points the default route
// of the container at the gateway of that network with ip inside the
// container, which must be privileged and carry iproute2. A container that is
// on the network already is not an error.
func (c Client[F]) NetworkConnect(ctx context.Context, network, container string) error {
	if _, err := c.Output(ctx, nil, "network", "connect", network, container); err != nil {
		if ok, checkErr := c.containerOnNetwork(ctx, container, network); checkErr != nil || !ok {
			return fmt.Errorf("%s: connect %q to %q: %w", c.dialect().Binary, container, network, err)
		}
	}

	gw, err := c.NetworkGateway(ctx, network)
	if err != nil {
		return fmt.Errorf("%s: gateway of %q: %w", c.dialect().Binary, network, err)
	}
	route := defaultRouteArgs(gw)
	if route == nil {
		return nil // an IPv6-only network has no IPv4 default route to set
	}
	if _, err = c.Exec(ctx, container, route...); err != nil {
		return fmt.Errorf("%s: route %q through %q: %w", c.dialect().Binary, container, network, err)
	}
	return nil
}

// defaultRouteArgs builds the ip command that points the default route of a
// container at gw. It returns nil when gw has no IPv4 address.
func defaultRouteArgs(gw container.Gateway) []string {
	if !gw.V4.IsValid() {
		return nil
	}
	return []string{"ip", "route", "replace", "default", "via", gw.V4.String()}
}

// containerOnNetwork reports if the container is already connected to the network.
func (c Client[F]) containerOnNetwork(ctx context.Context, container, network string) (bool, error) {
	out, err := c.Output(ctx, nil, "inspect", "--type", "container",
		"--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return false, fmt.Errorf("%s: inspect %q: %w", c.dialect().Binary, container, err)
	}

	var networks map[string]json.RawMessage
	if jsonErr := json.Unmarshal([]byte(out), &networks); jsonErr != nil {
		return false, fmt.Errorf("%s: inspect %q: decode: %w", c.dialect().Binary, container, jsonErr)
	}
	_, ok := networks[network]
	return ok, nil
}

// runArgs builds the engine arguments for a spec. The arguments are stable
// across calls for one spec.
func runArgs(spec container.RunSpec) []string {
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
func processArgs(spec container.RunSpec) []string {
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

// buildArgs builds the engine arguments for an image build.
func buildArgs(spec container.BuildSpec) []string {
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

// Build implements [container.Runtime] for the engine. The output of the build, on
// both streams, goes to out as it arrives.
func (c Client[F]) Build(ctx context.Context, spec container.BuildSpec, out io.Writer) error {
	args := buildArgs(spec)
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, c.dialect().Binary, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := c.runCmd(ctx, cmd); err != nil {
		return fmt.Errorf("%s: build %q: %w", c.dialect().Binary, spec.Tag, err)
	}
	return nil
}

// Run implements [container.Runtime] for the engine.
func (c Client[F]) Run(ctx context.Context, spec container.RunSpec) (string, error) {
	out, err := c.Output(ctx, nil, runArgs(spec)...)
	if err != nil {
		return "", friendlyRunErr(c.dialect(), fmt.Errorf("%s: run %q: %w", c.dialect().Binary, spec.Name, err), spec)
	}
	return strings.TrimSpace(out), nil
}

// friendlyRunErr attaches a human-facing message to err when its text names
// one of the run failures users hit most often - a port already in
// use, or an image that couldn't be found or pulled. It returns err
// unchanged for anything else: guessing at an unfamiliar engine error is
// worse than showing its raw text.
func friendlyRunErr(d Dialect, err error, spec container.RunSpec) error {
	msg := err.Error()
	switch {
	case containsAny(msg, d.PortInUse):
		return uerr.Wrap(err, "a port %s needs is already in use on this machine - stop whatever is using it, or change the step's published ports", spec.Name)
	case containsAny(msg, d.ImageMissing):
		return uerr.Wrap(err, "the image %q couldn't be found or pulled - check the name and tag, and that you're logged in if it's private", spec.Image)
	default:
		return err
	}
}

// containsAny reports whether s contains any of substrs.
func containsAny(s string, substrs []string) bool {
	return slices.ContainsFunc(substrs, func(sub string) bool { return strings.Contains(s, sub) })
}

// Remove implements [container.Runtime] for the engine.
func (c Client[F]) Remove(ctx context.Context, name string) error {
	if _, err := c.Output(ctx, nil, "rm", "--force", "--volumes", name); err != nil {
		if ok, existsErr := c.containerExists(ctx, name); existsErr == nil && !ok {
			return nil
		}
		return fmt.Errorf("%s: remove %q: %w", c.dialect().Binary, name, err)
	}
	return nil
}

// containerExists asks the engine for the container by exact name, rather than
// inferring absence from the wording of an error message.
func (c Client[F]) containerExists(ctx context.Context, name string) (bool, error) {
	out, err := c.Output(ctx, nil, "ps", "--all", "--format", "{{.Names}}",
		"--filter", "name=^"+name+"$")
	if err != nil {
		return false, fmt.Errorf("%s: list containers: %w", c.dialect().Binary, err)
	}
	return slices.Contains(strings.Split(strings.TrimSpace(out), "\n"), name), nil
}

// inspectResult mirrors the fields of inspect that kevin reads. A
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

// Inspect implements [container.Runtime] for the engine.
func (c Client[F]) Inspect(ctx context.Context, name string) (container.Container, error) {
	out, err := c.Output(ctx, nil, "inspect", "--type", "container", "--format", "{{json .}}", name)
	if err != nil {
		if ok, existsErr := c.containerExists(ctx, name); existsErr == nil && !ok {
			return container.Container{}, fmt.Errorf("%s: inspect %q: %w", c.dialect().Binary, name, container.ErrNotFound)
		}
		return container.Container{}, fmt.Errorf("%s: inspect %q: %w", c.dialect().Binary, name, err)
	}

	var raw inspectResult
	if jsonErr := json.Unmarshal([]byte(out), &raw); jsonErr != nil {
		return container.Container{}, fmt.Errorf("%s: inspect %q: decode: %w", c.dialect().Binary, name, jsonErr)
	}

	return fromInspect(raw), nil
}

func fromInspect(raw inspectResult) container.Container {
	c := container.Container{
		ID:       raw.ID,
		Name:     strings.TrimPrefix(raw.Name, "/"),
		Running:  raw.State.Running,
		Exited:   container.StatusExited(raw.State.Status),
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
func (c Client[F]) ListByLabel(ctx context.Context, key, value string) ([]string, error) {
	// No --quiet here. The flag overrides --format, and the output becomes a
	// list of IDs.
	out, err := c.Output(ctx, nil, "ps", "--all", "--no-trunc",
		"--format", "{{.Names}}", "--filter", "label="+key+"="+value)
	if err != nil {
		return nil, fmt.Errorf("%s: list containers: %w", c.dialect().Binary, err)
	}

	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Exec runs a command inside a container and returns its standard output.
func (c Client[F]) Exec(ctx context.Context, container string, args ...string) (string, error) {
	return c.ExecInput(ctx, container, nil, args...)
}

// ExecInput runs a command inside a container, with stdin feeding the
// command, and returns its standard output.
func (c Client[F]) ExecInput(ctx context.Context, name string, stdin io.Reader, args ...string) (string, error) {
	full := make([]string, 0, len(args)+3)
	full = append(full, "exec")
	if stdin != nil {
		full = append(full, "-i")
	}
	full = append(full, name)
	full = append(full, args...)

	out, err := c.Output(ctx, stdin, full...)
	if err != nil {
		if ok, existsErr := c.containerExists(ctx, name); existsErr == nil && !ok {
			return "", fmt.Errorf("%s: exec %q: %w", c.dialect().Binary, name, container.ErrNotFound)
		}
		return "", fmt.Errorf("%s: exec %q: %w", c.dialect().Binary, name, err)
	}
	return out, nil
}

// Save streams an image as a tar archive - the format
// nodeutils.LoadImageArchive expects. The caller must close the returned
// reader; Close waits for the engine process to exit.
//
// This bypasses run/Exec deliberately: those buffer the whole output as a
// string, and an image archive can be hundreds of megabytes.
func (c Client[F]) Save(ctx context.Context, image string) (io.ReadCloser, error) {
	ctx, cancel := context.WithCancel(ctx)
	//nolint:gosec // image is a locally-built tag, not user input
	cmd := exec.CommandContext(ctx, c.dialect().Binary, "save", image)
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

// saveReader stops the save process and waits for it to exit when the
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

// Output calls the engine binary and returns the standard output. A nil stdin
// gives the command no standard input. Errors carry the binary name as their
// label, so a docker run reports "docker:" and a podman run "podman:".
// Output is exported for the docker and podman packages, which add commands
// of their own.
func (c Client[F]) Output(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	//nolint:gosec // every argument comes from the environment definition
	cmd := exec.CommandContext(ctx, c.dialect().Binary, args...)
	cmd.Stdin = stdin

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := c.runCmd(ctx, cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", fmt.Errorf("%s %s: %w", c.dialect().Binary, strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("%s %s: %s: %w", c.dialect().Binary, strings.Join(args, " "), msg, err)
	}
	return stdout.String(), nil
}

// runCmd runs cmd with the Runner of c, or with command.Default when c has
// none.
func (c Client[F]) runCmd(ctx context.Context, cmd *exec.Cmd) error {
	if c.Runner == nil {
		return command.Run(ctx, cmd)
	}
	return c.Runner.Run(ctx, cmd)
}
