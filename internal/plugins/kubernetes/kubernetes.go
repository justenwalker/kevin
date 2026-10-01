// Package kubernetes runs a Kubernetes cluster as a step. A driver, named by
// the with block, creates the cluster with a cluster tool such as kind. The
// rest of the step is the same for every driver.
//
// The nodes keep the network that the cluster tool creates, and also join the
// project network, which carries their default route, thus a container step
// and a pod reach each other and egress leaves through the project network.
package kubernetes

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/engines"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// Step is the kubernetes step.
type Step struct{}

// New returns the kubernetes step.
func New() Step { return Step{} }

// Step must keep satisfying plugin.Step.
var _ plugin.Step = Step{}

// Schema constrains the with block of a kubernetes step.
func (Step) Schema() []byte { return schema }

// Kind reports that a kubernetes step creates and destroys a resource.
func (Step) Kind() plugin.StepKind { return plugin.StepKindResource }

// Step must keep satisfying plugin.IdempotentStep.
var _ plugin.IdempotentStep = Step{}

// Idempotent reports that a kubernetes step is idempotent.
func (Step) Idempotent() bool { return true }

// Up creates the cluster.
func (Step) Up(ctx context.Context, req *plugin.UpRequest, out plugin.Emitter) (*plugin.Result, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	wait, err := time.ParseDuration(cfg.Wait)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: wait %q: %w", cfg.Wait, err)
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if err = os.MkdirAll(filepath.Dir(kubeconfig), 0o700); err != nil {
		return nil, fmt.Errorf("kubernetes: create the kubeconfig directory: %w", err)
	}

	rt, err := engines.New(req.Env.Engine, req.Env.EngineConfig)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: %w", err)
	}

	drv, err := newDriver(cfg, req.Env, name, kubeconfig, rt)
	if err != nil {
		return nil, err
	}

	nodes, err := reuseOrCreateCluster(ctx, drv, name, kubeconfig, wait, out)
	if err != nil {
		return nil, err
	}

	if err = joinProjectNetwork(ctx, rt, nodes, req.Env.Network); err != nil {
		return nil, err
	}

	if err = drv.RefreshAccess(ctx); err != nil {
		return nil, err
	}

	if err = drv.LabelNodes(ctx); err != nil {
		return nil, err
	}

	fwd := clusterrelay.ForwarderSpec{
		Name: clusterrelay.ForwarderName(name), Network: req.Env.Network,
		Project: req.Env.Project, Scope: req.Env.Scope, Step: req.Step,
	}
	setup, err := finishClusterSetup(ctx, rt, drv, cfg, req.Env, nodes, fwd, out)
	if err != nil {
		return nil, err
	}

	outputs := clusterOutputs(drv, name, kubeconfig, nodes)
	if wantsRelay(cfg) {
		outputs["relay_addr"] = setup.RelayAddr
	} else if err = rt.Remove(ctx, fwd.Name); err != nil {
		return nil, fmt.Errorf("kubernetes: remove the stale relay forwarder for %q: %w", name, err)
	}

	return &plugin.Result{
		ExposedPorts: setup.Exposed,
		Outputs:      plugin.StringMap(outputs),
		EgressAllow:  cfg.Egress,
		Details:      exposedPortDetails(setup.Exposed),
		Containers:   setup.Containers,
	}, nil
}

// configMarkerFile is where reuseOrCreateCluster persists the fingerprint
// name was last created with, alongside kubeconfig - what decides whether a
// persistent cluster can be reused as-is.
func configMarkerFile(kubeconfig string) string { return kubeconfig + ".config" }

// reuseOrCreateCluster reuses a running cluster of that name whose
// fingerprint is unchanged, and otherwise creates a fresh one.
func reuseOrCreateCluster(ctx context.Context, drv driver, name, kubeconfig string, wait time.Duration, out plugin.Emitter) ([]string, error) {
	existingNodes, err := drv.Nodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: check for an existing cluster %q: %w", name, err)
	}

	if len(existingNodes) > 0 {
		// A cluster created against one proxy address must not be reused
		// against another: the proxy env is baked into the nodes once, at
		// creation, and nothing updates it afterward. The fingerprint
		// covers it.
		wantConfig, fingerprintErr := drv.Fingerprint(createSpec{Wait: wait})
		if fingerprintErr != nil {
			return nil, fingerprintErr
		}
		if marker, readErr := os.ReadFile(configMarkerFile(kubeconfig)); readErr == nil && string(marker) == wantConfig {
			out.Log("stdout", fmt.Sprintf("reusing cluster %s with %d node(s)", name, len(existingNodes)))
			return existingNodes, nil
		}
	}

	spec := createSpec{Wait: wait}
	nodeList, err := drv.Create(ctx, spec, out)
	if err != nil {
		return nil, err
	}
	marker, err := drv.Fingerprint(spec)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(configMarkerFile(kubeconfig), []byte(marker), 0o600); err != nil {
		return nil, fmt.Errorf("kubernetes: write the cluster config marker for %q: %w", name, err)
	}
	return nodeList, nil
}

// clusterSetup is what finishClusterSetup adds to the result of Up.
type clusterSetup struct {
	Exposed    []plugin.ExposedPort
	Containers []plugin.ContainerInfo
	RelayAddr  string
}

// finishClusterSetup installs the trust CA, patches CoreDNS, registers
// egress capture, and finishes the relay, each only when the config wants
// it.
func finishClusterSetup(ctx context.Context, rt cri.Runtime, drv driver, cfg config, env plugin.Env, nodes []string, fwd clusterrelay.ForwarderSpec, out plugin.Emitter) (clusterSetup, error) {
	// The proxy intercepts TLS for a pull. A node trusts the kevin root
	// certificate, so the pull verifies.
	if wantsTrustCA(cfg, env) {
		if err := trustCAFromPath(ctx, drv, nodes, env.CAPath, out); err != nil {
			return clusterSetup{}, err
		}
	}

	// A relay that is off, or an environment with no domain, needs no patch. A
	// cluster must still come up in that case.
	if wantsCoreDNSPatch(cfg, env) {
		if err := patchCoreDNS(ctx, drv, nodes, env.Domain, env.Relay, out); err != nil {
			return clusterSetup{}, err
		}
	}

	var setup clusterSetup
	if wantsCapture(env) {
		var err error
		if setup.Containers, err = nodeContainers(ctx, rt, drv, nodes, out); err != nil {
			return clusterSetup{}, err
		}
	}

	if !wantsRelay(cfg) {
		return setup, nil
	}
	relayed, err := finishRelay(ctx, rt, drv, cfg, fwd, out)
	if err != nil {
		return clusterSetup{}, err
	}
	setup.Exposed, setup.RelayAddr = relayed.Exposed, relayed.Addr
	return setup, nil
}

// clusterOutputs builds the values that Up publishes for dependent steps.
func clusterOutputs(drv driver, name, kubeconfig string, nodeList []string) map[string]string {
	return map[string]string{
		"name":       name,
		"kubeconfig": kubeconfig,
		"context":    drv.Context(),
		"nodes":      strings.Join(nodeList, ","),
	}
}

// Down removes the cluster and its files in the workspace.
func (Step) Down(ctx context.Context, req *plugin.DownRequest, out plugin.Emitter) error {
	cfg, err := decode(req.Config)
	if err != nil {
		return err
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)

	// An unusable engine leaves rt nil: the driver still removes the cluster.
	rt, _ := engines.New(req.Env.Engine, req.Env.EngineConfig)
	drv, err := newDriver(cfg, req.Env, name, kubeconfig, rt)
	if err != nil {
		return err
	}

	out.Log("stdout", "removing cluster "+name)

	if rt != nil {
		if err = rt.Remove(ctx, clusterrelay.ForwarderName(name)); err != nil {
			return fmt.Errorf("kubernetes: remove the relay forwarder for %q: %w", name, err)
		}
	}

	if err = drv.Delete(ctx, out); err != nil {
		return err
	}

	// The kubeconfig of a cluster that is gone points at nothing.
	if err = os.Remove(kubeconfig); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("kubernetes: remove %s: %w", kubeconfig, err)
	}
	if err = os.Remove(configMarkerFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("kubernetes: remove %s: %w", configMarkerFile(kubeconfig), err)
	}
	return nil
}

// Export reports the name, kubeconfig, context, relay_addr, and live node
// containers of the cluster. Containers stays empty when the nodes cannot
// be listed.
func (Step) Export(ctx context.Context, req *plugin.ExportRequest) (*plugin.ExportResult, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if _, statErr := os.Stat(kubeconfig); statErr != nil {
		return nil, fmt.Errorf("kubernetes: cluster %q has no kubeconfig yet, run `kevin run` or `kevin setup` first: %w", name, statErr)
	}

	// An unusable engine leaves rt nil: Export still reports Out, without
	// containers.
	rt, _ := engines.New(req.Env.Engine, req.Env.EngineConfig)
	drv, err := newDriver(cfg, req.Env, name, kubeconfig, rt)
	if err != nil {
		return nil, err
	}

	out := map[string]string{
		"name":       name,
		"kubeconfig": kubeconfig,
		"context":    drv.Context(),
	}
	if rt != nil {
		fwd, ok, lookupErr := clusterrelay.LookupForwarder(ctx, rt, clusterrelay.ForwarderName(name))
		if lookupErr != nil {
			return nil, fmt.Errorf("kubernetes: look up the relay forwarder for %q: %w", name, lookupErr)
		}
		if ok {
			out["relay_addr"] = fwd.Addr
		}
	}

	return &plugin.ExportResult{
		Out:        plugin.StringMap(out),
		Containers: exportContainers(ctx, rt, drv),
	}, nil
}

// exportContainers reports the cluster's current per-node containers for
// Export - nil when there is no engine or the nodes can't be listed (the
// cluster has been torn down since Up), matching Up's own fail-open
// handling of a live-inspect problem it can't resolve.
func exportContainers(ctx context.Context, rt cri.Runtime, drv driver) []plugin.ContainerInfo {
	if rt == nil {
		return nil
	}
	nodeList, err := drv.Nodes(ctx)
	if err != nil || len(nodeList) == 0 {
		// The cluster may be gone since Up - the driver reports that as an
		// empty node list, not an error. Export still reports Out from the
		// workspace files either way.
		return nil
	}
	containers, err := nodeContainers(ctx, rt, drv, nodeList, nil)
	if err != nil {
		return nil
	}
	return containers
}
