// Package container implements the builtin:container step.
//
// A container is a resource step that manages a container lifecycle.
package container

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// caPath is where the plugin mounts the kevin CA inside a container.
const caPath = "/usr/local/share/ca-certificates/kevin.crt"

// pollInterval is the wait between two checks of the container state.
const pollInterval = 100 * time.Millisecond

// config is the decoded with block of one step.
type config struct {
	Image        string            `json:"image"`
	Pull         bool              `json:"pull"`
	Cmd          []string          `json:"cmd"`
	Entrypoint   []string          `json:"entrypoint"`
	Env          map[string]string `json:"env"`
	Ports        []string          `json:"ports"`
	Volumes      []string          `json:"volumes"`
	Proxy        bool              `json:"proxy"`
	Egress       []string          `json:"egress"`
	StartTimeout string            `json:"start_timeout"`
	Expose       []expose          `json:"expose"`
}

// expose is one entry of the with block's expose list: a container port to
// publish directly to the host as raw TCP or UDP.
type expose struct {
	Port     int    `json:"port"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	HostPort int    `json:"host_port"`
}

// Container is the container step.
type Container struct{}

// New returns a container step.
func New() Container { return Container{} }

// Container must keep satisfying plugin.Step.
var _ plugin.Step = Container{}

// Schema constrains the with block of a container step.
func (Container) Schema() []byte { return schema }

// Kind reports that a container step creates and destroys a resource.
func (Container) Kind() plugin.StepKind { return plugin.StepKindResource }

// Container must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Container{}

// Idempotent reports that a container step is idempotent. Up always removes
// any stale container of the same name before creating a fresh one.
func (Container) Idempotent() bool { return true }

// Up creates the container.
func (Container) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	timeout, err := time.ParseDuration(cfg.StartTimeout)
	if err != nil {
		return nil, fmt.Errorf("container: start_timeout %q: %w", cfg.StartTimeout, err)
	}
	deadline := time.Now().Add(timeout)

	runtime, err := newRuntime(req.Env)
	if err != nil {
		return nil, err
	}

	name := containerName(req.Env.Project, req.Step)

	// A previous run can leave a container behind. Remove it first so that a
	// second Up does not fail on the name.
	if err = runtime.Remove(ctx, name); err != nil {
		return nil, err
	}

	spec := cri.RunSpec{
		Image:      cfg.Image,
		Name:       name,
		Network:    req.Env.Network,
		Alias:      req.Step,
		Pull:       cfg.Pull,
		Cmd:        cfg.Cmd,
		Entrypoint: cfg.Entrypoint,
		Ports:      buildPorts(cfg),
		Volumes:    cfg.Volumes,
		Labels: map[string]string{
			cri.LabelProject: req.Env.Project,
			cri.LabelScope:   cri.ScopeLabel(req.Env.Project, req.Env.Scope),
			cri.LabelURN:     cri.URNLabel(req.Env.Project, req.Env.Scope, req.Step),
		},
		Env: buildEnv(cfg, req),
	}
	if cfg.Proxy && req.Env.CAPath != "" {
		spec.Volumes = append(spec.Volumes, req.Env.CAPath+":"+caPath+":ro")
	}
	if req.Env.Relay != "" {
		spec.DNS = []string{req.Env.Relay}
	}

	out.Log("stdout", "starting "+cfg.Image)
	id, err := runtime.Run(ctx, spec)
	if err != nil {
		return nil, err
	}

	info, err := waitRunning(ctx, runtime, name, deadline, out)
	if err != nil {
		return nil, err
	}

	out.Log("stdout", "running as "+name)

	exposed, err := exposedPorts(cfg, info)
	if err != nil {
		return nil, err
	}

	return &plugin.Result{
		Outputs:      plugin.StringMap(outputs(id, name, info, req.Env.Network)),
		ExposedPorts: exposed,
		EgressAllow:  cfg.Egress,
		Details:      stepDetails(exposed),
	}, nil
}

// buildPorts is the full list of ports to publish: the step's own ports:
// list, plus one entry per expose declaration.
func buildPorts(cfg config) []string {
	ports := slices.Clone(cfg.Ports)
	for _, e := range cfg.Expose {
		ports = append(ports, publishSpec(e))
	}
	return ports
}

// publishSpec builds a docker --publish string for one expose entry, on the
// loopback like every other published port here - the host process that
// reaches it is on the host, not the docker network.
func publishSpec(e expose) string {
	host := ""
	if e.HostPort > 0 {
		host = strconv.Itoa(e.HostPort)
	}
	spec := "127.0.0.1:" + host + ":" + strconv.Itoa(e.Port)
	if e.Protocol == "udp" {
		spec += "/udp"
	}
	return spec
}

// stepDetails mirrors every exposed port onto the step's card.
func stepDetails(exposed []plugin.ExposedPort) []plugin.Detail {
	details := make([]plugin.Detail, 0, len(exposed))
	for _, ep := range exposed {
		details = append(details, ep.Detail())
	}
	return details
}

// exposedPorts reports the raw TCP/UDP endpoints that this step's Up
// publishes directly to the host, from its expose declarations.
func exposedPorts(cfg config, info cri.Container) ([]plugin.ExposedPort, error) {
	out := make([]plugin.ExposedPort, 0, len(cfg.Expose))
	for _, e := range cfg.Expose {
		upstream, ok := info.Ports[strconv.Itoa(e.Port)+"/"+e.Protocol]
		if !ok {
			return nil, plugin.Wrap(
				fmt.Errorf("container: expose %s %d: the container publishes no such port: %w", e.Protocol, e.Port, ErrNoPort),
				"the container isn't listening on %s %s - check the image actually exposes it, or fix the expose block", e.Protocol, strconv.Itoa(e.Port))
		}
		name := e.Name
		if name == "" {
			name = strconv.Itoa(e.Port)
		}
		out = append(out, plugin.ExposedPort{Name: name, Protocol: e.Protocol, Upstream: upstream})
	}
	return out, nil
}

// Down removes the container.
func (Container) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	runtime, err := newRuntime(req.Env)
	if err != nil {
		return err
	}
	name := containerName(req.Env.Project, req.Step)
	out.Log("stdout", "removing "+name)
	return runtime.Remove(ctx, name)
}

// newRuntime picks the container engine that env names.
var newRuntime = func(env plugin.Env) (cri.Runtime, error) { // a var so tests can substitute a fake engine
	switch env.Engine {
	case "", "docker":
		return docker.New(env.EngineConfig)
	default:
		return nil, plugin.Wrap(
			fmt.Errorf("container: unsupported engine %q: %w", env.Engine, ErrUnsupportedEngine),
			"kevin only supports the docker engine today (got %q) - remove engine from kevin.cue, or set it to \"docker\"", env.Engine)
	}
}

// containerName builds the container name for one step of one project.
func containerName(project, step string) string {
	return "kevin-" + project + "-" + step
}

// buildEnv merges the environment of the step with the proxy variables.
// Step variables take precedence over proxy variables.
func buildEnv(cfg config, req *plugin.UpRequest) map[string]string {
	env := make(map[string]string, len(cfg.Env)+len(req.Env.ProxyEnv)+1)
	maps.Copy(env, cfg.Env)
	if !cfg.Proxy {
		return env
	}
	for k, v := range req.Env.ProxyEnv {
		if _, ok := env[k]; !ok {
			env[k] = v
		}
	}
	if req.Env.CAPath != "" {
		if _, ok := env["SSL_CERT_FILE"]; !ok {
			env["SSL_CERT_FILE"] = caPath
		}
	}
	return env
}

// waitRunning polls until the container runs.
// Returns [ErrExited] when the container stops first.
// Returns [context.DeadlineExceeded] when the deadline passes.
func waitRunning(ctx context.Context, runtime cri.Runtime, name string, deadline time.Time, out plugin.Emitter) (cri.Container, error) {
	out.Progress("waiting for "+name, 0, 0)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	for {
		info, err := runtime.Inspect(ctx, name)
		if err != nil {
			return cri.Container{}, err
		}
		if info.Running {
			return info, nil
		}
		if info.ExitCode != 0 {
			return info, fmt.Errorf("container %q exited with code %d: %w", name, info.ExitCode, ErrExited)
		}

		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return cri.Container{}, ctx.Err() //nolint:wrapcheck // context.Canceled/DeadlineExceeded is the idiomatic bare sentinel
		}
	}
}

// outputs builds the values that a dependent step reads.
// A published port appears as "host_<port>", such as "host_80".
func outputs(id, name string, info cri.Container, network string) map[string]string {
	values := map[string]string{
		"id":   id,
		"name": name,
	}
	if ip, ok := info.IPs[network]; ok {
		values["ip"] = ip
	}
	for port, addr := range info.Ports {
		values["host_"+trimProto(port)] = addr
	}
	return values
}

// trimProto turns "80/tcp" into "80".
func trimProto(port string) string {
	for i, r := range port {
		if r == '/' {
			return port[:i]
		}
	}
	return port
}

func decode(data []byte) (config, error) {
	// Repeat the defaults of schema.cue. A caller can pass an empty config,
	// and then CUE never applies them.
	cfg := config{Proxy: true, StartTimeout: "30s"}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("container: decode config: %w", err)
	}
	// Repeat schema.cue's per-entry default too, for the same reason.
	for i, e := range cfg.Expose {
		if e.Protocol == "" {
			cfg.Expose[i].Protocol = "tcp"
		}
	}
	return cfg, nil
}
