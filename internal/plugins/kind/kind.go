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
	"os"
	"path/filepath"
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
	Name    string       `json:"name"`
	Image   string       `json:"image"`
	Workers int          `json:"workers"`
	Config  string       `json:"config"`
	Wait    string       `json:"wait"`
	Retain  bool         `json:"retain"`
	Proxy   bool         `json:"proxy"`
	Egress  []string     `json:"egress"`
	CoreDNS bool         `json:"coredns"`
	TrustCA bool         `json:"trust_ca"`
	Expose  []kindExpose `json:"expose"`
	Relay   bool         `json:"relay"`
}

// kindExpose is one entry of the with block's expose list: an in-cluster
// address to reach through the SOCKS5 relay.
type kindExpose struct {
	Address string `json:"address"`
	Name    string `json:"name"`
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

	wait, err := time.ParseDuration(cfg.Wait)
	if err != nil {
		return nil, fmt.Errorf("kind: wait %q: %w", cfg.Wait, err)
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if err = os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		return nil, fmt.Errorf("kind: create the kubeconfig directory: %w", err)
	}

	// A relay's host port must be picked before cluster creation: unlike a
	// container's port publish, kind's node port mappings are fixed at
	// creation time.
	useRelay := wantsRelay(cfg)
	relayHostPort, relayAddress, err := prepareRelayPort(ctx, useRelay)
	if err != nil {
		return nil, err
	}

	nodeList, err := createCluster(ctx, cfg, req, name, kubeconfig, wait, relayHostPort, out)
	if err != nil {
		return nil, err
	}

	exposedPorts, err := finishClusterSetup(ctx, cfg, req, name, nodeList, relayAddress, useRelay, out)
	if err != nil {
		return nil, err
	}

	outputs := clusterOutputs(name, kubeconfig, nodeList)
	if useRelay {
		outputs["relay_addr"] = relayAddress
	}

	return &plugin.Result{
		ExposedPorts: exposedPorts,
		Outputs:      plugin.StringMap(outputs),
		EgressAllow:  cfg.Egress,
		Details:      exposedPortDetails(exposedPorts),
	}, nil
}

// prepareRelayPort picks the relay's host port before cluster creation, when
// a relay is wanted. It reports a zero port and an empty address otherwise.
func prepareRelayPort(ctx context.Context, useRelay bool) (int, string, error) {
	if !useRelay {
		return 0, "", nil
	}
	hostPort, err := findFreePort(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("kind: pick a port for the relay: %w", err)
	}
	return hostPort, relayAddr(hostPort), nil
}

// createCluster removes a stale cluster of the same name, creates a fresh
// one, and joins its nodes to the shared network.
func createCluster(ctx context.Context, cfg config, req *plugin.UpRequest, name, kubeconfig string, wait time.Duration, relayHostPort int, out plugin.Emitter) ([]string, error) {
	// A cluster of this name may survive a crash. Ensure it is deleted before
	// we bring it up again - kind delete cluster is documented as idempotent,
	// a no-op success when the cluster is already gone.
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
	return nil
}

// Export reports the kubeconfig path that Up writes for this cluster.
// Export does not touch Docker or Kubernetes; it fails only when the
// cluster has never come up.
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

	return &plugin.ExportResult{Env: map[string]string{"KUBECONFIG": kubeconfig}}, nil
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
