// Package kind runs a Kubernetes cluster as a step, backed by kind.
//
// The nodes join the shared network of the project directly, in place of
// kind's own default network, thus a container step and a pod reach each
// other.
package kind

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/engines"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/plugin"
)

//go:embed schema.cue
var schema []byte

// kindProviderEnvVar is kind's own switch (upstream-labeled experimental)
// between its docker and podman node-container providers.
const kindProviderEnvVar = "KIND_EXPERIMENTAL_PROVIDER"

// kindNetworkEnvVar is kind's own switch (upstream-labeled experimental) for
// the docker network its nodes join, in place of kind's own fixed "kind"
// network - pointed at the project's shared network, so a node never touches
// a network shared with another project's nodes.
const kindNetworkEnvVar = "KIND_EXPERIMENTAL_DOCKER_NETWORK"

// nodeLabelKey is the Kubernetes node label kevin applies to every node -
// the control-plane node gets controlPlaneNodeName, a worker node gets its
// own key from with.workers. nodeNames (capture.go) reads it back.
const nodeLabelKey = cri.LabelPrefix + "node"

// controlPlaneNodeName is the fixed nodeLabelKey value for the cluster's
// one control-plane node - not user-configurable, since there's only ever
// one, nothing to disambiguate.
const controlPlaneNodeName = "control-plane"

// providerEnv reports the KIND_EXPERIMENTAL_PROVIDER addition every kindcmd
// call for one cluster needs when the project's engine is podman - kind's
// node-container operations (create, delete, get nodes, load
// image-archive) all go through this switch, not just creation. nil for
// docker, kind's own default.
func providerEnv(env plugin.Env) map[string]string {
	if env.Engine != "podman" {
		return nil
	}
	return map[string]string{kindProviderEnvVar: "podman"}
}

// mergeEnv combines a and b into a fresh map, b winning on a shared key.
// Either may be nil.
func mergeEnv(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]string, len(a)+len(b))
	maps.Copy(merged, a)
	maps.Copy(merged, b)
	return merged
}

// config is the decoded with block of one step.
type config struct {
	Name         string                    `json:"name"`
	Image        string                    `json:"image"`
	ControlPlane map[string]any            `json:"control_plane"`
	Workers      map[string]map[string]any `json:"workers"`
	Config       string                    `json:"config"`
	Wait         string                    `json:"wait"`
	Retain       bool                      `json:"retain"`
	Proxy        bool                      `json:"proxy"`
	Egress       []string                  `json:"egress"`
	CoreDNS      bool                      `json:"coredns"`
	TrustCA      bool                      `json:"trust_ca"`
	Expose       map[string]kindExpose     `json:"expose"`
	Relay        bool                      `json:"relay"`
}

// kindExpose is one entry of the with block's expose map: an in-cluster
// address to reach through the SOCKS5 relay. The map key is its name.
type kindExpose struct {
	Address  string `json:"address"`
	Protocol string `json:"protocol"`
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
	resolveMountPaths(cfg.ControlPlane, req.Env.ProjectDir)
	for _, w := range cfg.Workers {
		resolveMountPaths(w, req.Env.ProjectDir)
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

	rt, err := engines.New(req.Env.Engine, req.Env.EngineConfig)
	if err != nil {
		return nil, fmt.Errorf("kind: %w", err)
	}

	nodeList, ports, err := reuseOrCreateCluster(ctx, cfg, req, name, kubeconfig, wait, useRelay, out)
	if err != nil {
		return nil, err
	}
	relayAddress := ""
	if useRelay {
		relayAddress = relayAddr(ports.TCP)
	}

	exposedPorts, containers, err := finishClusterSetup(ctx, rt, cfg, req, name, nodeList, relayAddress, ports.UDP, useRelay, out)
	if err != nil {
		return nil, err
	}

	outputs := clusterOutputs(name, kubeconfig, nodeList)
	if useRelay {
		outputs["relay_addr"] = relayAddress
	}
	if err = persistRelayPorts(kubeconfig, name, relayAddress, ports.UDP, useRelay); err != nil {
		return nil, err
	}

	return &plugin.Result{
		ExposedPorts: exposedPorts,
		Outputs:      plugin.StringMap(outputs),
		EgressAllow:  cfg.Egress,
		Details:      exposedPortDetails(exposedPorts),
		Containers:   containers,
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

// resolveMountPaths rewrites node["extraMounts"][*]["hostPath"] entries that
// are relative, against projectDir - a no-op when node has no extraMounts
// key, when an entry isn't shaped like a mount, or when a path is already
// absolute.
func resolveMountPaths(node map[string]any, projectDir string) {
	mounts, ok := node["extraMounts"].([]any)
	if !ok {
		return
	}
	for _, m := range mounts {
		mount, ok := m.(map[string]any)
		if !ok {
			continue
		}
		hostPath, ok := mount["hostPath"].(string)
		if !ok {
			continue
		}
		mount["hostPath"] = resolvePath(hostPath, projectDir)
	}
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

// relayUDPAddrFile is where Up persists the relay's UDP pool host ports,
// alongside kubeconfig - readRelayUDPPorts's counterpart to
// readRelayPort/relayAddrFile for the TCP port.
func relayUDPAddrFile(kubeconfig string) string { return kubeconfig + ".relay-udp-ports" }

// writeRelayUDPPorts persists ports as a comma-separated list (empty when
// the pool is disabled) for readRelayUDPPorts to read back on a later Up.
func writeRelayUDPPorts(kubeconfig string, ports []int) error {
	strs := make([]string, len(ports))
	for i, p := range ports {
		strs[i] = strconv.Itoa(p)
	}
	return os.WriteFile(relayUDPAddrFile(kubeconfig), []byte(strings.Join(strs, ",")), 0o600) //nolint:wrapcheck // caller wraps with the cluster name
}

// readRelayUDPPorts reads back the UDP pool host ports a previous Up
// reserved, from relayUDPAddrFile. It reports ok false when the file is
// missing or does not parse, the same "not reusable" signal
// readRelayPort's own ok reports - an empty-but-present file is a valid,
// reusable "pool disabled" state, not a failure to read.
func readRelayUDPPorts(kubeconfig string) ([]int, bool) {
	data, err := os.ReadFile(relayUDPAddrFile(kubeconfig))
	if err != nil {
		return nil, false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil, true
	}
	parts := strings.Split(text, ",")
	ports := make([]int, len(parts))
	for i, p := range parts {
		port, convErr := strconv.Atoi(p)
		if convErr != nil {
			return nil, false
		}
		ports[i] = port
	}
	return ports, true
}

// reuseOrCreateCluster reports the node list and relay ports (the zero
// value when useRelay is false) for name, reusing an already-running
// cluster in place when one exists and its config has not changed since
// the last Up - a persistent setup-scope cluster must not be destroyed and
// rebuilt on every "kevin setup", only when its own config actually
// changed. It falls back to createCluster (delete, then create fresh) in
// every other case.
func reuseOrCreateCluster(ctx context.Context, cfg config, req *plugin.UpRequest, name, kubeconfig string, wait time.Duration, useRelay bool, out plugin.Emitter) ([]string, relayPorts, error) {
	existingNodes, err := kindcmd.GetNodes(ctx, name, providerEnv(req.Env))
	if err != nil {
		return nil, relayPorts{}, fmt.Errorf("kind: check for an existing cluster %q: %w", name, err)
	}

	// Relay ports can only be reused, never freshly picked, without also
	// invalidating the comparison below - the config text embeds whichever
	// ports the variable holds, so fresh, different ones would never match
	// a marker file written by the run that actually created the live
	// cluster.
	ports, reusablePorts := reusableRelayPorts(kubeconfig, useRelay)

	if len(existingNodes) > 0 && reusablePorts {
		// A cluster created against one proxy address must not be reused
		// against another - kind bakes the proxy env into containerd once,
		// at creation, and nothing updates it afterward.
		wantConfig, fingerprintErr := reuseFingerprint(cfg, ports, proxyEnv(cfg, req.Env))
		if fingerprintErr != nil {
			return nil, relayPorts{}, fingerprintErr
		}
		if marker, readErr := os.ReadFile(configMarkerFile(kubeconfig)); readErr == nil && string(marker) == wantConfig {
			out.Log("stdout", fmt.Sprintf("reusing cluster %s with %d node(s)", name, len(existingNodes)))
			return existingNodes, ports, nil
		}
	}

	if useRelay {
		if ports, err = pickRelayPorts(ctx); err != nil {
			return nil, relayPorts{}, err
		}
	}
	nodeList, err := createCluster(ctx, cfg, req, name, kubeconfig, wait, ports, out)
	if err != nil {
		return nil, relayPorts{}, err
	}
	marker, err := reuseFingerprint(cfg, ports, proxyEnv(cfg, req.Env))
	if err != nil {
		return nil, relayPorts{}, err
	}
	if err = os.WriteFile(configMarkerFile(kubeconfig), []byte(marker), 0o600); err != nil {
		return nil, relayPorts{}, fmt.Errorf("kind: write the cluster config marker for %q: %w", name, err)
	}
	return nodeList, ports, nil
}

// reusableRelayPorts reads back the relay ports a previous Up reserved for
// kubeconfig, when useRelay - reusablePorts is false when useRelay is true
// but either the TCP port or the UDP pool can't be read back, meaning the
// caller must pick fresh ones and cannot reuse an existing cluster as-is.
func reusableRelayPorts(kubeconfig string, useRelay bool) (relayPorts, bool) {
	if !useRelay {
		return relayPorts{}, true
	}
	tcp, tcpOK := readRelayPort(kubeconfig)
	udp, udpOK := readRelayUDPPorts(kubeconfig)
	return relayPorts{TCP: tcp, UDP: udp}, tcpOK && udpOK
}

// pickRelayPorts asks the OS for a fresh TCP relay port and, when
// relay.UDPPoolSize() is nonzero, a fresh UDP ASSOCIATE pool of that size.
func pickRelayPorts(ctx context.Context) (relayPorts, error) {
	tcp, err := findFreePort(ctx)
	if err != nil {
		return relayPorts{}, fmt.Errorf("kind: pick a port for the relay: %w", err)
	}
	poolSize, err := relay.UDPPoolSize()
	if err != nil {
		return relayPorts{}, fmt.Errorf("kind: %w", err)
	}
	if poolSize == 0 {
		return relayPorts{TCP: tcp}, nil
	}
	udp, err := findFreePorts(ctx, poolSize)
	if err != nil {
		return relayPorts{}, fmt.Errorf("kind: pick ports for the relay's udp pool: %w", err)
	}
	return relayPorts{TCP: tcp, UDP: udp}, nil
}

// reuseFingerprint reports the generated kind config plus the resolved
// proxy endpoint, as a single comparable string.
func reuseFingerprint(cfg config, ports relayPorts, proxy map[string]string) (string, error) {
	generated, err := clusterConfig(cfg, ports)
	if err != nil {
		return "", err
	}
	return generated + "\n# proxy=" + proxy["HTTP_PROXY"], nil
}

// createCluster removes a stale cluster of the same name, then creates a
// fresh one directly on the project's shared network.
func createCluster(ctx context.Context, cfg config, req *plugin.UpRequest, name, kubeconfig string, wait time.Duration, ports relayPorts, out plugin.Emitter) ([]string, error) {
	provider := providerEnv(req.Env)

	// A cluster of this name may survive a crash, or reuseOrCreateCluster may
	// have found one whose config changed. Ensure it is deleted before we
	// bring it up again - kind delete cluster is documented as idempotent, a
	// no-op success when the cluster is already gone.
	if err := kindcmd.Delete(ctx, kindcmd.DeleteSpec{Name: name, Kubeconfig: kubeconfig, Env: provider}, plugin.NewLineWriter(out, "stderr")); err != nil {
		return nil, fmt.Errorf("kind: remove the previous cluster %q: %w", name, err)
	}

	out.Log("stdout", "creating cluster "+name)
	out.Progress("creating "+name, 0, 0)

	generatedConfig, err := clusterConfig(cfg, ports)
	if err != nil {
		return nil, err
	}

	env := mergeEnv(proxyEnv(cfg, req.Env), provider)
	env = mergeEnv(env, map[string]string{kindNetworkEnvVar: req.Env.Network})
	spec := kindcmd.CreateSpec{
		Name:       name,
		Kubeconfig: kubeconfig,
		Config:     generatedConfig,
		Wait:       wait,
		Retain:     cfg.Retain,
		Image:      cfg.Image,
		Env:        env,
	}
	if err = kindcmd.Create(ctx, spec, plugin.NewLineWriter(out, "stdout"), plugin.NewLineWriter(out, "stderr")); err != nil {
		return nil, fmt.Errorf("kind: create the cluster %q: %w", name, err)
	}

	nodeList, err := kindcmd.GetNodes(ctx, name, provider)
	if err != nil {
		return nil, fmt.Errorf("kind: list the nodes of %q: %w", name, err)
	}
	if len(nodeList) == 0 {
		return nil, fmt.Errorf("kind: cluster %q: %w", name, ErrNoNodes)
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

// finishClusterSetup installs the trust CA, patches CoreDNS, registers
// egress capture, and finishes the relay, each only when the config wants
// it.
func finishClusterSetup(ctx context.Context, rt cri.Runtime, cfg config, req *plugin.UpRequest, name string, nodeList []string, relayAddress string, relayUDPHostPorts []int, useRelay bool, out plugin.Emitter) ([]plugin.ExposedPort, []plugin.ContainerInfo, error) {
	// The proxy intercepts TLS for a pull. A node trusts the kevin root
	// certificate, so the pull verifies.
	if wantsTrustCA(cfg, req.Env) {
		if err := trustCAFromPath(ctx, rt, nodeList, req.Env.CAPath, out); err != nil {
			return nil, nil, err
		}
	}

	// A relay that is off, or an environment with no domain, needs no patch. A
	// cluster must still come up in that case.
	if wantsCoreDNSPatch(cfg, req.Env) {
		if err := patchCoreDNS(ctx, rt, nodeList, req.Env.Domain, req.Env.Relay, out); err != nil {
			return nil, nil, err
		}
	}

	var containers []plugin.ContainerInfo
	if wantsCapture(req.Env) {
		var err error
		if containers, err = nodeContainers(ctx, rt, nodeList, out); err != nil {
			return nil, nil, err
		}
	}

	if !useRelay {
		return nil, containers, nil
	}
	exposedPorts, err := finishRelay(ctx, rt, cfg, name, nodeList, relayAddress, relayUDPHostPorts, req.Env, out)
	if err != nil {
		return nil, nil, err
	}
	return exposedPorts, containers, nil
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

// persistRelayPorts writes relayAddress and udpHostPorts to their marker
// files when useRelay, or removes any stale ones from a previous Up
// otherwise - readRelayPort/readRelayUDPPorts's counterpart, called once
// Up knows the final result.
func persistRelayPorts(kubeconfig, name, relayAddress string, udpHostPorts []int, useRelay bool) error {
	if !useRelay {
		if err := os.Remove(relayAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("kind: remove the stale relay address for %q: %w", name, err)
		}
		if err := os.Remove(relayUDPAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("kind: remove the stale relay udp pool for %q: %w", name, err)
		}
		return nil
	}
	if err := os.WriteFile(relayAddrFile(kubeconfig), []byte(relayAddress), 0o600); err != nil {
		return fmt.Errorf("kind: write the relay address for %q: %w", name, err)
	}
	if err := writeRelayUDPPorts(kubeconfig, udpHostPorts); err != nil {
		return fmt.Errorf("kind: write the relay udp pool for %q: %w", name, err)
	}
	return nil
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

	if err = kindcmd.Delete(ctx, kindcmd.DeleteSpec{Name: name, Kubeconfig: kubeconfig, Env: providerEnv(req.Env)}, plugin.NewLineWriter(out, "stderr")); err != nil {
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
// Export reads what Up already wrote to the workspace (kubeconfig,
// relay_addr) with no live docker/kubectl calls, except for Containers:
// reporting each node's current container identity needs a live GetNodes
// and Inspect (via nodeContainers, the same helper Up uses) - the one
// piece Export can't answer from a static file. A discovery failure fails
// open (nil Containers, no error), same as Up: a cross-scope
// builtin:fault target simply finds nothing to resolve against, rather
// than an unrelated "${setup.<name>.out...}" reference failing over it.
func (Step) Export(ctx context.Context, req *plugin.ExportRequest) (*plugin.ExportResult, error) {
	cfg, err := decode(req.Config)
	if err != nil {
		return nil, err
	}

	name := clusterName(cfg, req.Env.Project, req.Step)
	kubeconfig := filepath.Join(req.Env.Workspace, "kubeconfig", name)
	if _, statErr := os.Stat(kubeconfig); statErr != nil {
		return nil, fmt.Errorf("kind: cluster %q has no kubeconfig yet, run `kevin run` or `kevin setup` first: %w", name, statErr)
	}

	out := map[string]string{
		"name":       name,
		"kubeconfig": kubeconfig,
		"context":    "kind-" + name,
	}
	if relayAddress, readErr := os.ReadFile(relayAddrFile(kubeconfig)); readErr == nil {
		out["relay_addr"] = string(relayAddress)
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("kind: read the relay address for %q: %w", name, readErr)
	}

	containers, err := exportContainers(ctx, req, name)
	if err != nil {
		return nil, err
	}

	return &plugin.ExportResult{
		Out:        plugin.StringMap(out),
		Containers: containers,
	}, nil
}

// exportContainers reports the cluster's current per-node containers for
// Export - nil, with no error, when the nodes can't be listed at all (the
// cluster has been torn down since Up, or the engine is unreachable),
// matching Up's own fail-open handling of a live-inspect problem it
// can't resolve.
func exportContainers(ctx context.Context, req *plugin.ExportRequest, name string) ([]plugin.ContainerInfo, error) {
	rt, err := engines.New(req.Env.Engine, req.Env.EngineConfig)
	if err != nil {
		return nil, nil //nolint:nilerr // an unusable engine is nothing Export can report containers against
	}
	nodeList, err := kindcmd.GetNodes(ctx, name, providerEnv(req.Env))
	if err != nil || len(nodeList) == 0 {
		// The cluster may be gone since Up - kind reports that as an empty
		// node list, not an error. Export still reports Out from the
		// workspace files either way.
		return nil, nil //nolint:nilerr // see comment above
	}
	return nodeContainers(ctx, rt, nodeList, nil)
}

// clusterName builds the name of the cluster. kind prefixes every container
// with "kind-", thus the name stays short.
func clusterName(cfg config, project, step string) string {
	if cfg.Name != "" {
		return cfg.Name
	}
	return project + "-" + step
}

// buildNode merges passthrough (a control_plane or workers entry, or nil)
// with kevin's own required fields for one node: role always wins over
// passthrough (ErrReservedNodeField if passthrough sets it - it's
// structural, not configurable); labels combine, with nodeLabelKey reserved
// the same way (ErrReservedNodeField if passthrough's own labels sets it);
// extraPortMappings combine by concatenation, kevin's own entries first;
// every other passthrough key copies through unchanged.
func buildNode(role string, kevinLabels map[string]string, kevinPortMappings []map[string]any, passthrough map[string]any) (map[string]any, error) {
	if _, ok := passthrough["role"]; ok {
		return nil, fmt.Errorf("kind: node config: %w: %q", ErrReservedNodeField, "role")
	}

	node := make(map[string]any, len(passthrough)+2)
	maps.Copy(node, passthrough)
	node["role"] = role

	labels := make(map[string]any, len(kevinLabels))
	for k, v := range kevinLabels {
		labels[k] = v
	}
	if raw, ok := passthrough["labels"]; ok {
		userLabels, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("kind: node config: labels: %w", ErrInvalidNodeField)
		}
		for k, v := range userLabels {
			if k == nodeLabelKey {
				return nil, fmt.Errorf("kind: node config: labels: %w: %q", ErrReservedNodeField, nodeLabelKey)
			}
			labels[k] = v
		}
	}
	node["labels"] = labels

	if len(kevinPortMappings) > 0 {
		merged := make([]any, 0, len(kevinPortMappings))
		for _, m := range kevinPortMappings {
			merged = append(merged, m)
		}
		if raw, ok := passthrough["extraPortMappings"]; ok {
			userMappings, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("kind: node config: extraPortMappings: %w", ErrInvalidNodeField)
			}
			merged = append(merged, userMappings...)
		}
		node["extraPortMappings"] = merged
	}

	return node, nil
}

// clusterConfig returns the kind configuration for a step. An explicit
// config wins over the generated one, thus a hand-written config is on its
// own for extraPortMappings too, the same as it already is for workers.
//
// ports, when set, adds an extraPortMappings entry per reserved port to
// the control-plane node - one for the TCP relay gateway, one per UDP
// ASSOCIATE pool port - for the SOCKS5 relay deployRelay starts after the
// cluster comes up. These must be baked in here, before creation - unlike
// a container's port publish, kind's node port mappings are fixed at
// cluster creation and cannot be added later.
func clusterConfig(cfg config, ports relayPorts) (string, error) {
	if strings.TrimSpace(cfg.Config) != "" {
		return cfg.Config, nil
	}

	var relayMappings []map[string]any
	if ports.TCP > 0 {
		relayMappings = append(relayMappings, map[string]any{
			"containerPort": relayNodePort,
			"hostPort":      ports.TCP,
			"listenAddress": "127.0.0.1",
			"protocol":      "TCP",
		})
	}
	for i, hostPort := range ports.UDP {
		relayMappings = append(relayMappings, map[string]any{
			"containerPort": relayUDPNodePortBase + i,
			"hostPort":      hostPort,
			"listenAddress": "127.0.0.1",
			"protocol":      "UDP",
		})
	}

	controlPlane, err := buildNode("control-plane", map[string]string{nodeLabelKey: controlPlaneNodeName}, relayMappings, cfg.ControlPlane)
	if err != nil {
		return "", err
	}
	nodes := []map[string]any{controlPlane}

	for _, name := range slices.Sorted(maps.Keys(cfg.Workers)) {
		var worker map[string]any
		worker, err = buildNode("worker", map[string]string{nodeLabelKey: name}, nil, cfg.Workers[name])
		if err != nil {
			return "", err
		}
		nodes = append(nodes, worker)
	}

	doc := map[string]any{
		"kind":       "Cluster",
		"apiVersion": "kind.x-k8s.io/v1alpha4",
		"nodes":      nodes,
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("kind: marshal the cluster config: %w", err)
	}
	return string(out), nil
}

// wantsCoreDNSPatch reports whether Up must patch the cluster DNS. A relay
// that is off, or an environment with no domain, needs no patch.
func wantsCoreDNSPatch(cfg config, env plugin.Env) bool {
	return cfg.CoreDNS && env.Relay != "" && env.Domain != ""
}

func decode(data []byte) (config, error) {
	cfg := config{Wait: "5m", Proxy: true, CoreDNS: true, TrustCA: true}
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("kind: decode config: %w", err)
	}
	// Repeat schema.cue's per-entry default, for a caller that bypasses CUE.
	for name, e := range cfg.Expose {
		if e.Protocol == "" {
			e.Protocol = "tcp"
			cfg.Expose[name] = e
		}
	}
	return cfg, nil
}
