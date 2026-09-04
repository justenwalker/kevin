// Package kind runs a Kubernetes cluster as a step, backed by kind.
//
// The nodes join the shared network of the project as well as the network of
// kind, thus a container step and a pod reach each other.
package kind

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/docker"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// dockerClient runs container operations against node containers, through
// the generic cri.Runtime contract. kind always runs its nodes as plain
// docker containers, regardless of the project's configured engine, so the
// docker implementation - not req.Env's engine - is always the right one
// here.
var dockerClient cri.Runtime = docker.Client{}

// config is the decoded with block of one step.
type config struct {
	Name        string           `json:"name"`
	Image       string           `json:"image"`
	Workers     int              `json:"workers"`
	Config      string           `json:"config"`
	Wait        string           `json:"wait"`
	Retain      bool             `json:"retain"`
	Proxy       bool             `json:"proxy"`
	Egress      []string         `json:"egress"`
	CoreDNS     bool             `json:"coredns"`
	TrustCA     bool             `json:"trust_ca"`
	Expose      []kindExpose     `json:"expose"`
	Relay       bool             `json:"relay"`
	ExtraMounts []kindExtraMount `json:"extra_mounts"`
}

// kindExtraMount is one entry of the with block's extra_mounts list: a host
// directory bind-mounted into the control-plane node, generated config
// only - ignored the same way workers is when config is set.
type kindExtraMount struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
}

// kindExpose is one entry of the with block's expose list: an in-cluster
// address to reach through the SOCKS5 relay.
type kindExpose struct {
	Address  string `json:"address"`
	Name     string `json:"name"`
	HostPort int    `json:"host_port"`
}

// Step is the kind step.
type Step struct{}

// New returns the kind step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a kind step.
func (Step) Schema() []byte { return schema }

// Kind reports that a kind step creates and destroys a resource.
func (Step) Kind() plugin.StepKind { return plugin.StepKindResource }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a kind step is idempotent. Up always removes any
// stale cluster of the same name before creating a fresh one.
func (Step) Idempotent() bool { return true }

// Up creates the cluster.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}
	for i := range cfg.ExtraMounts {
		cfg.ExtraMounts[i].HostPath = resolvePath(cfg.ExtraMounts[i].HostPath, req.Env.ProjectDir)
	}

	wait, err := time.ParseDuration(cfg.Wait)
	if err != nil {
		return nil, fmt.Errorf("kind: wait %q: %w", cfg.Wait, err)
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if err = os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		return nil, fmt.Errorf("kind: create the kubeconfig directory: %w", err)
	}

	useRelay := wantsRelay(cfg)

	nodeList, relayHostPort, err := reuseOrCreateCluster(ctx, cfg, req, name, kubeconfig, wait, useRelay, out)
	if err != nil {
		return nil, err
	}
	relayAddress := ""
	if useRelay {
		relayAddress = relayAddr(relayHostPort)
	}

	exposedPorts, err := finishClusterSetup(ctx, cfg, req, name, nodeList, relayAddress, useRelay, out)
	if err != nil {
		return nil, err
	}

	outputs := clusterOutputs(name, kubeconfig, nodeList)
	if useRelay {
		outputs["relay_addr"] = relayAddress
		if err = os.WriteFile(relayAddrFile(kubeconfig), []byte(relayAddress), 0o600); err != nil {
			return nil, fmt.Errorf("kind: write the relay address for %q: %w", name, err)
		}
	} else if err = os.Remove(relayAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("kind: remove the stale relay address for %q: %w", name, err)
	}

	return &plugin.Result{
		ExposedPorts: exposedPorts,
		Outputs:      plugin.StringMap(outputs),
		EgressAllow:  cfg.Egress,
		Details:      exposedPortDetails(exposedPorts),
	}, nil
}

// resolvePath resolves a with-block path against the project directory. An
// absolute path, an empty path, or a missing project directory pass through
// unchanged. Mirrors the identical helper in internal/plugins/kubectl and
// internal/plugins/helm.
func resolvePath(path, projectDir string) string {
	if path == "" || projectDir == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(projectDir, path)
}

// configMarkerFile is where reuseOrCreateCluster persists the kind config
// text it last created name with, alongside kubeconfig - the fingerprint
// clusterMatches compares against to decide whether a persistent cluster
// can be reused as-is.
func configMarkerFile(kubeconfig string) string { return kubeconfig + ".config" }

// readRelayPort reads back the host port that a previous Up picked for
// name's relay, from the relay address Up persists at relayAddrFile. It
// reports ok false when no relay was set up last time, or the file does not
// parse - either way, the caller must treat that as "no reusable port", not
// an error: kind's node port mappings are fixed at cluster creation, so a
// mismatched or missing port means the cluster cannot be reused unchanged.
func readRelayPort(kubeconfig string) (int, bool) {
	addr, err := os.ReadFile(relayAddrFile(kubeconfig))
	if err != nil {
		return 0, false
	}
	_, portStr, err := net.SplitHostPort(strings.TrimSpace(string(addr)))
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, false
	}
	return port, true
}

// reuseOrCreateCluster reports the node list and relay host port (0 when
// useRelay is false) for name, reusing an already-running cluster in place
// when one exists and its config has not changed since the last Up - a
// persistent setup-scope cluster must not be destroyed and rebuilt on every
// "kevin setup", only when its own config actually changed. It falls back
// to createCluster (delete, then create fresh) in every other case.
func reuseOrCreateCluster(ctx context.Context, cfg config, req *plugin.UpRequest, name, kubeconfig string, wait time.Duration, useRelay bool, out plugin.Emitter) ([]string, int, error) {
	existingNodes, err := kindcmd.GetNodes(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("kind: check for an existing cluster %q: %w", name, err)
	}

	// A relay port can only be reused, never freshly picked, without also
	// invalidating the comparison below - the config text embeds whichever
	// port relayHostPort holds, so a fresh, different port would never
	// match a marker file written by the run that actually created the
	// live cluster.
	relayHostPort := 0
	reusablePort := true
	if useRelay {
		relayHostPort, reusablePort = readRelayPort(kubeconfig)
	}

	if len(existingNodes) > 0 && reusablePort {
		wantConfig := clusterConfig(cfg, relayHostPort)
		if marker, readErr := os.ReadFile(configMarkerFile(kubeconfig)); readErr == nil && string(marker) == wantConfig {
			if err = joinSharedNetwork(ctx, req.Env.Network, existingNodes); err != nil {
				return nil, 0, err
			}
			out.Log("stdout", fmt.Sprintf("reusing cluster %s with %d node(s)", name, len(existingNodes)))
			return existingNodes, relayHostPort, nil
		}
	}

	if useRelay {
		if relayHostPort, err = findFreePort(ctx); err != nil {
			return nil, 0, fmt.Errorf("kind: pick a port for the relay: %w", err)
		}
	}
	nodeList, err := createCluster(ctx, cfg, req, name, kubeconfig, wait, relayHostPort, out)
	if err != nil {
		return nil, 0, err
	}
	if err = os.WriteFile(configMarkerFile(kubeconfig), []byte(clusterConfig(cfg, relayHostPort)), 0o600); err != nil {
		return nil, 0, fmt.Errorf("kind: write the cluster config marker for %q: %w", name, err)
	}
	return nodeList, relayHostPort, nil
}

// createCluster removes a stale cluster of the same name, creates a fresh
// one, and joins its nodes to the shared network.
func createCluster(ctx context.Context, cfg config, req *plugin.UpRequest, name, kubeconfig string, wait time.Duration, relayHostPort int, out plugin.Emitter) ([]string, error) {
	// A cluster of this name may survive a crash, or reuseOrCreateCluster may
	// have found one whose config changed. Ensure it is deleted before we
	// bring it up again - kind delete cluster is documented as idempotent, a
	// no-op success when the cluster is already gone.
	if err := kindcmd.Delete(ctx, kindcmd.DeleteSpec{Name: name, Kubeconfig: kubeconfig}, stderrWriter(out)); err != nil {
		return nil, fmt.Errorf("kind: remove the previous cluster %q: %w", name, err)
	}

	out.Log("stdout", "creating cluster "+name)
	out.Progress("creating "+name, 0, 0)

	spec := kindcmd.CreateSpec{
		Name:       name,
		Kubeconfig: kubeconfig,
		Config:     clusterConfig(cfg, relayHostPort),
		Wait:       wait,
		Retain:     cfg.Retain,
		Image:      cfg.Image,
		Env:        proxyEnv(cfg, req.Env),
	}
	if err := kindcmd.Create(ctx, spec, stdoutWriter(out), stderrWriter(out)); err != nil {
		return nil, fmt.Errorf("kind: create the cluster %q: %w", name, err)
	}

	nodeList, err := kindcmd.GetNodes(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("kind: list the nodes of %q: %w", name, err)
	}
	if len(nodeList) == 0 {
		return nil, fmt.Errorf("kind: cluster %q: %w", name, ErrNoNodes)
	}

	// kind puts the nodes on a network of its own. Join the shared network as
	// well, so that a container step and a pod reach each other.
	if err = joinSharedNetwork(ctx, req.Env.Network, nodeList); err != nil {
		return nil, err
	}

	out.Log("stdout", fmt.Sprintf("cluster %s is ready with %d node(s)", name, len(nodeList)))
	return nodeList, nil
}

// proxyEnv reports the proxy variables to set for kindcmd.Create, or nil
// when the step's with block turns the proxy off, or the environment has
// none configured - kindcmd.Create then leaves the child's own environment
// untouched.
func proxyEnv(cfg config, env plugin.Env) map[string]string {
	if !cfg.Proxy || len(env.ProxyEnv) == 0 {
		return nil
	}
	return env.ProxyEnv
}

// finishClusterSetup installs the trust CA, patches CoreDNS, and finishes
// the relay, each only when the config wants it.
func finishClusterSetup(ctx context.Context, cfg config, req *plugin.UpRequest, name string, nodeList []string, relayAddress string, useRelay bool, out plugin.Emitter) ([]plugin.ExposedPort, error) {
	// The proxy intercepts TLS for a pull. A node trusts the kevin root
	// certificate, so the pull verifies.
	if wantsTrustCA(cfg, req.Env) {
		if err := trustCAFromPath(ctx, nodeList, req.Env.CAPath, out); err != nil {
			return nil, err
		}
	}

	// A relay that is off, or an environment with no domain, needs no patch. A
	// cluster must still come up in that case.
	if wantsCoreDNSPatch(cfg, req.Env) {
		if err := patchCoreDNS(ctx, nodeList, req.Env.Domain, req.Env.Relay, out); err != nil {
			return nil, err
		}
	}

	if !useRelay {
		return nil, nil
	}
	return finishRelay(ctx, cfg, name, nodeList, relayAddress, out)
}

// clusterOutputs builds the values that Up publishes for dependent steps.
func clusterOutputs(name, kubeconfig string, nodeList []string) map[string]string {
	return map[string]string{
		"name":       name,
		"kubeconfig": kubeconfig,
		"context":    "kind-" + name,
		"nodes":      strings.Join(nodeList, ","),
	}
}

// relayAddrFile is where Up persists the relay's host:port, alongside
// kubeconfig.
func relayAddrFile(kubeconfig string) string { return kubeconfig + ".relay-addr" }

// Down removes the cluster.
func (Step) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)

	out.Log("stdout", "removing cluster "+name)

	if err = kindcmd.Delete(ctx, kindcmd.DeleteSpec{Name: name, Kubeconfig: kubeconfig}, stderrWriter(out)); err != nil {
		return fmt.Errorf("kind: remove the cluster %q: %w", name, err)
	}

	// The kubeconfig of a cluster that is gone points at nothing.
	if err = os.Remove(kubeconfig); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("kind: remove %s: %w", kubeconfig, err)
	}
	if err = os.Remove(relayAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("kind: remove %s: %w", relayAddrFile(kubeconfig), err)
	}
	if err = os.Remove(configMarkerFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("kind: remove %s: %w", configMarkerFile(kubeconfig), err)
	}
	return nil
}

// Export reports the kubeconfig path, name, context, and relay_addr (read
// back from relayAddrFile) for this cluster. It touches neither Docker nor
// Kubernetes, so it can't report nodes, and fails only when the cluster
// has never come up.
func (Step) Export(_ context.Context, req *plugin.ExportRequest) (*plugin.ExportResult, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil, fmt.Errorf("kind: cluster %q has no kubeconfig yet, run `kevin run` or `kevin setup` first: %w", name, err)
	}

	out := map[string]string{
		"name":       name,
		"kubeconfig": kubeconfig,
		"context":    "kind-" + name,
	}
	if relayAddress, err := os.ReadFile(relayAddrFile(kubeconfig)); err == nil {
		out["relay_addr"] = string(relayAddress)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("kind: read the relay address for %q: %w", name, err)
	}

	return &plugin.ExportResult{
		Out: plugin.StringMap(out),
	}, nil
}

// clusterName builds the name of the cluster. kind prefixes every container
// with "kind-", thus the name stays short.
func clusterName(cfg config, project, step string) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return project + "-" + step
}

// clusterConfig returns the kind configuration for a step. An explicit
// config wins over the generated one, thus a hand-written config is on its
// own for extraPortMappings too, the same as it already is for workers.
//
// relayHostPort, when nonzero, adds one extraPortMappings entry to the
// control-plane node, for the SOCKS5 relay deployRelay starts after the
// cluster comes up. It must be baked in here, before creation - unlike a
// container's port publish, kind's node port mappings are fixed at cluster
// creation and cannot be added later.
func clusterConfig(cfg config, relayHostPort int) string {
	if strings.TrimSpace(cfg.Config) != "" {
		return cfg.Config
	}

	var b strings.Builder
	b.WriteString("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnodes:\n")
	b.WriteString("- role: control-plane\n")
	if relayHostPort > 0 {
		fmt.Fprintf(&b, "  extraPortMappings:\n  - containerPort: %d\n    hostPort: %d\n    listenAddress: \"127.0.0.1\"\n    protocol: TCP\n",
			relayNodePort, relayHostPort)
	}
	if len(cfg.ExtraMounts) > 0 {
		b.WriteString("  extraMounts:\n")
		for _, m := range cfg.ExtraMounts {
			fmt.Fprintf(&b, "  - hostPath: %s\n    containerPath: %s\n",
				strconv.Quote(m.HostPath), strconv.Quote(m.ContainerPath))
		}
	}
	for range cfg.Workers {
		b.WriteString("- role: worker\n")
	}
	return b.String()
}

// wantsCoreDNSPatch reports whether Up must patch the cluster DNS. A relay
// that is off, or an environment with no domain, needs no patch.
func wantsCoreDNSPatch(cfg config, env plugin.Env) bool {
	return cfg.CoreDNS && env.Relay != "" && env.Domain != ""
}

// joinSharedNetwork connects every node to the shared docker network, so
// that a container step and a pod reach each other.
func joinSharedNetwork(ctx context.Context, network string, allNodes []string) error {
	for _, node := range allNodes {
		if err := dockerClient.NetworkConnect(ctx, network, node); err != nil {
			return err
		}
	}
	return nil
}

func decode(data []byte) (config, error) {
	cfg := config{Wait: "5m", Proxy: true, CoreDNS: true, TrustCA: true}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("kind: decode config: %w", err)
	}
	return cfg, nil
}
