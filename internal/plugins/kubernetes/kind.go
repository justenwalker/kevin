package kubernetes

import (
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/plugin"
)

// kindProviderEnvVar is kind's own switch (upstream-labeled experimental)
// between its docker and podman node-container providers.
const kindProviderEnvVar = "KIND_EXPERIMENTAL_PROVIDER"

// kindDriver creates the cluster with the kind command. kind runs each node
// as a container, so a command in a node goes through the container
// runtime.
type kindDriver struct {
	cfg        config
	env        plugin.Env
	name       string
	kubeconfig string
	rt         cri.Runtime
}

var _ driver = (*kindDriver)(nil)

// newKindDriver returns the kind driver for one cluster. It resolves the
// relative mount paths of the node config against the project directory.
func newKindDriver(cfg config, env plugin.Env, name, kubeconfig string, rt cri.Runtime) *kindDriver {
	resolveMountPaths(cfg.Kind.ControlPlane, env.ProjectDir)
	for _, w := range cfg.Workers {
		resolveMountPaths(w, env.ProjectDir)
	}
	return &kindDriver{cfg: cfg, env: env, name: name, kubeconfig: kubeconfig, rt: rt}
}

// Nodes lists the node containers of the cluster.
func (d *kindDriver) Nodes(ctx context.Context) ([]string, error) {
	nodes, err := kindcmd.GetNodes(ctx, d.name, providerEnv(d.env))
	if err != nil {
		return nil, fmt.Errorf("kubernetes: kind: list the nodes of %q: %w", d.name, err)
	}
	return nodes, nil
}

// Fingerprint reports the generated kind config plus the resolved proxy
// endpoint, as a single comparable string. kind bakes the proxy env into
// containerd once, at creation, and nothing updates it afterward.
func (d *kindDriver) Fingerprint(spec createSpec) (string, error) {
	generated, err := clusterConfig(d.cfg, spec.Ports)
	if err != nil {
		return "", err
	}
	return generated + "\n# proxy=" + proxyEnv(d.cfg, d.env.ProxyEnv)["HTTP_PROXY"], nil
}

// Create removes a stale cluster of the same name, then creates a fresh
// one.
func (d *kindDriver) Create(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error) {
	provider := providerEnv(d.env)

	// A cluster of this name may survive a crash, or the caller may have
	// found one whose config changed. Ensure it is deleted before we bring
	// it up again - kind delete cluster is documented as idempotent, a
	// no-op success when the cluster is already gone.
	if err := d.Delete(ctx, out); err != nil {
		return nil, fmt.Errorf("kubernetes: kind: remove the previous cluster %q: %w", d.name, err)
	}

	out.Log("stdout", "creating cluster "+d.name)
	out.Progress("creating "+d.name, 0, 0)

	generatedConfig, err := clusterConfig(d.cfg, spec.Ports)
	if err != nil {
		return nil, err
	}

	env := mergeEnv(proxyEnv(d.cfg, d.env.ProxyEnv), provider)
	create := kindcmd.CreateSpec{
		Name:       d.name,
		Kubeconfig: d.kubeconfig,
		Config:     generatedConfig,
		Wait:       spec.Wait,
		Retain:     d.cfg.Retain,
		Image:      d.cfg.Kind.Image,
		Env:        env,
	}
	if err = kindcmd.Create(ctx, create, plugin.NewLineWriter(out, "stdout"), plugin.NewLineWriter(out, "stderr")); err != nil {
		return nil, fmt.Errorf("kubernetes: kind: create the cluster %q: %w", d.name, err)
	}

	nodeList, err := d.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodeList) == 0 {
		return nil, fmt.Errorf("kubernetes: kind: cluster %q: %w", d.name, ErrNoNodes)
	}

	out.Log("stdout", fmt.Sprintf("cluster %s is ready with %d node(s)", d.name, len(nodeList)))
	return nodeList, nil
}

// Delete removes the cluster.
func (d *kindDriver) Delete(ctx context.Context, out plugin.Emitter) error {
	spec := kindcmd.DeleteSpec{Name: d.name, Kubeconfig: d.kubeconfig, Env: providerEnv(d.env)}
	if err := kindcmd.Delete(ctx, spec, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kubernetes: kind: delete the cluster %q: %w", d.name, err)
	}
	return nil
}

// Context is "kind-" and the cluster name.
func (d *kindDriver) Context() string { return "kind-" + d.name }

// ControlPlane is the first control-plane node. kind names the first
// control-plane node "<cluster>-control-plane" and any further one
// "<cluster>-control-planeN", also for a hand-written config.
func (d *kindDriver) ControlPlane() string { return d.name + "-control-plane" }

// RefreshAccess does nothing: kind publishes the API server on a host port
// that it chose, which joining a network does not change.
func (*kindDriver) RefreshAccess(context.Context) error { return nil }

// LabelNodes does nothing: kind sets the labels at creation, from the
// generated config.
func (*kindDriver) LabelNodes(context.Context) error { return nil }

// LoadImage loads the image archive at path into the nodes.
func (d *kindDriver) LoadImage(ctx context.Context, path string, out plugin.Emitter) error {
	spec := kindcmd.LoadImageArchiveSpec{Name: d.name, Path: path, Env: providerEnv(d.env)}
	if err := kindcmd.LoadImageArchive(ctx, spec, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kubernetes: kind: load the image archive: %w", err)
	}
	return nil
}

// providerEnv reports the KIND_EXPERIMENTAL_PROVIDER addition every kindcmd
// call for one cluster needs when the project's engine is podman - kind's
// node-container operations (create, delete, get nodes, load
// image-archive) all go through this switch, not just creation. nil for
// docker, kind's own default.
func providerEnv(env plugin.Env) map[string]string {
	if env.Engine != enginePodman {
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

// adminKubeconfig is the kubeconfig that a node carries for its own cluster.
const adminKubeconfig = "/etc/kubernetes/admin.conf"

// Kubectl runs kubectl inside the control-plane node.
func (d *kindDriver) Kubectl(ctx context.Context, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: kind: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.Exec(ctx, d.ControlPlane(), kubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: kind: kubectl: %w", err)
	}
	return out, nil
}

// KubectlInput runs kubectl inside the control-plane node, with stdin
// feeding the command.
func (d *kindDriver) KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: kind: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.ExecInput(ctx, d.ControlPlane(), stdin, kubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: kind: kubectl: %w", err)
	}
	return out, nil
}

// kubectlArgs prepends the kubectl command and the admin kubeconfig flag to
// args.
func kubectlArgs(args []string) []string {
	full := make([]string, 0, len(args)+3)
	full = append(full, "kubectl", "--kubeconfig", adminKubeconfig)
	return append(full, args...)
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
		return nil, fmt.Errorf("kubernetes: kind: node config: %w: %q", ErrReservedNodeField, "role")
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
			return nil, fmt.Errorf("kubernetes: kind: node config: labels: %w", ErrInvalidNodeField)
		}
		for k, v := range userLabels {
			if k == nodeLabelKey {
				return nil, fmt.Errorf("kubernetes: kind: node config: labels: %w: %q", ErrReservedNodeField, nodeLabelKey)
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
				return nil, fmt.Errorf("kubernetes: kind: node config: extraPortMappings: %w", ErrInvalidNodeField)
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
func clusterConfig(cfg config, ports clusterrelay.Ports) (string, error) {
	if strings.TrimSpace(cfg.Kind.Config) != "" {
		return cfg.Kind.Config, nil
	}

	var relayMappings []map[string]any
	if ports.TCP > 0 {
		relayMappings = append(relayMappings, map[string]any{
			"containerPort": clusterrelay.NodePort,
			"hostPort":      ports.TCP,
			"listenAddress": "127.0.0.1",
			"protocol":      "TCP",
		})
	}
	for i, hostPort := range ports.UDP {
		relayMappings = append(relayMappings, map[string]any{
			"containerPort": clusterrelay.UDPNodePortBase + i,
			"hostPort":      hostPort,
			"listenAddress": "127.0.0.1",
			"protocol":      "UDP",
		})
	}

	controlPlane, err := buildNode("control-plane", map[string]string{nodeLabelKey: controlPlaneNodeName}, relayMappings, cfg.Kind.ControlPlane)
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
		return "", fmt.Errorf("kubernetes: kind: marshal the cluster config: %w", err)
	}
	return string(out), nil
}
