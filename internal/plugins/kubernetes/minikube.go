package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/internal/command"
	minikubecmd "github.com/justenwalker/kevin/internal/command/minikube"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/plugin"
)

// minikubeNodeLabel marks every container that minikube creates.
const minikubeNodeLabel = "created_by.minikube.sigs.k8s.io"

// minikubeKubectlScript runs the kubectl that minikube installs in a node,
// whose path holds the Kubernetes version.
const minikubeKubectlScript = `exec /var/lib/minikube/binaries/*/kubectl --kubeconfig ` + adminKubeconfig + ` "$@"`

// minikubeDriver creates the cluster with the minikube command. minikube
// runs each node as a container, so a command in a node goes through the
// container runtime.
type minikubeDriver struct {
	cfg        config
	env        plugin.Env
	name       string
	kubeconfig string
	rt         cri.Runtime

	// The minikube calls that Create, Delete and LoadImage make, and the
	// lookup of the cache that every cluster shares, held as values so that a
	// test can stub them.
	start         func(ctx context.Context, spec minikubecmd.StartSpec, stdout, stderr io.Writer) error
	deleteProfile func(ctx context.Context, name, home string, stderr io.Writer) error
	loadImage     func(ctx context.Context, name, home, path string, stderr io.Writer) error
	cacheDir      func() (string, error)
}

var _ driver = (*minikubeDriver)(nil)

// newMinikubeDriver returns the minikube driver for one cluster. It returns
// [ErrMinikubeWorkerSettings] when a worker carries node settings, and
// [ErrMinikubeMounts] when mounts has more than one entry. rt may be nil,
// and then Delete leaves the network of the cluster in place.
func newMinikubeDriver(cfg config, env plugin.Env, name, kubeconfig string, rt cri.Runtime) (*minikubeDriver, error) {
	for worker, settings := range cfg.Workers {
		if len(settings) > 0 {
			return nil, fmt.Errorf("worker %q: %w", worker, ErrMinikubeWorkerSettings)
		}
	}
	if len(cfg.Mounts) > 1 {
		return nil, fmt.Errorf("%d entries: %w", len(cfg.Mounts), ErrMinikubeMounts)
	}
	client := minikubecmd.New(command.Default)
	return &minikubeDriver{
		cfg: cfg, env: env, name: name, kubeconfig: kubeconfig, rt: rt,
		start:         client.Start,
		deleteProfile: client.Delete,
		loadImage:     client.ImageLoad,
		cacheDir:      minikubeCacheDir,
	}, nil
}

// minikubeCacheDir is the cache of the user's own minikube.
func minikubeCacheDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	return filepath.Join(home, ".minikube", "cache"), nil
}

// home is the MINIKUBE_HOME of the cluster. minikube appends ".minikube" to
// any other name.
func (d *minikubeDriver) home() string {
	return filepath.Join(d.env.Workspace, "minikube", d.name, ".minikube")
}

// engineDriver is the minikube driver that matches the project's engine.
func (d *minikubeDriver) engineDriver() string {
	if d.env.Engine == enginePodman {
		return enginePodman
	}
	return "docker"
}

// network is the name of the network that the nodes of the cluster join.
// Podman ignores a requested network and names its own after the profile.
// The docker prefix keeps Delete from removing a network of another name.
func (d *minikubeDriver) network() string {
	if d.env.Engine == enginePodman {
		return d.name
	}
	return "kevin-minikube-" + d.name
}

// workers lists the worker names in node creation order.
func (d *minikubeDriver) workers() []string { return slices.Sorted(maps.Keys(d.cfg.Workers)) }

// mountsCA reports whether Create installs the kevin root certificate in the
// nodes.
func (d *minikubeDriver) mountsCA() bool { return wantsTrustCA(d.cfg, d.env) }

// startSpec builds the minikube start arguments for the cluster.
func (d *minikubeDriver) startSpec(spec createSpec) minikubecmd.StartSpec {
	start := minikubecmd.StartSpec{
		Name:              d.name,
		Driver:            d.engineDriver(),
		Nodes:             1 + len(d.cfg.Workers),
		Wait:              spec.Wait,
		KubernetesVersion: d.cfg.Minikube.KubernetesVersion,
		BaseImage:         d.cfg.Minikube.BaseImage,
		Memory:            d.cfg.Minikube.Memory,
		CPUs:              d.cfg.Minikube.CPUs,
		Home:              d.home(),
		Kubeconfig:        d.kubeconfig,
	}
	if d.env.Engine != enginePodman {
		start.Network = d.network()
	}
	for _, m := range resolveMounts(d.cfg.Mounts, d.env.ProjectDir) {
		start.Mount = m.Host + ":" + m.Container
		if m.ReadOnly {
			start.Mount += ":ro"
		}
	}
	return start
}

// Nodes lists the node containers of the cluster: the control plane, then
// the workers in order.
func (d *minikubeDriver) Nodes(ctx context.Context) ([]string, error) {
	if d.rt == nil {
		return nil, fmt.Errorf("kubernetes: minikube: list the nodes of %q: %w", d.name, ErrNoRuntime)
	}
	all, err := d.rt.ListByLabel(ctx, minikubeNodeLabel, "true")
	if err != nil {
		return nil, fmt.Errorf("kubernetes: minikube: list the nodes of %q: %w", d.name, err)
	}

	workerName := regexp.MustCompile("^" + regexp.QuoteMeta(d.name) + "-m[0-9]{2,}$")
	var controlPlane bool
	var workers []string
	for _, name := range all {
		switch {
		case name == d.name:
			controlPlane = true
		case workerName.MatchString(name):
			workers = append(workers, name)
		}
	}
	// Shorter names first, so that -m10 sorts after -m09 and -m100 after -m99.
	slices.SortFunc(workers, func(a, b string) int {
		if len(a) != len(b) {
			return len(a) - len(b)
		}
		return strings.Compare(a, b)
	})

	var nodes []string
	if controlPlane {
		nodes = append(nodes, d.name)
	}
	return append(nodes, workers...), nil
}

// Fingerprint reports the exact minikube start arguments the cluster runs
// with, including the resolved proxy endpoint and the content of the kevin
// root certificate, as a single comparable string. Create bakes both into the
// nodes once, and nothing updates them afterward.
func (d *minikubeDriver) Fingerprint(spec createSpec) (string, error) {
	parts := minikubecmd.StartArgs(d.startSpec(spec))
	proxy := proxyEnv(d.cfg, d.env.ProxyEnv)
	for _, key := range slices.Sorted(maps.Keys(proxy)) {
		parts = append(parts, "#", key+"="+proxy[key])
	}
	if d.mountsCA() {
		pem, err := os.ReadFile(d.env.CAPath)
		if err != nil {
			return "", fmt.Errorf("kubernetes: minikube: read the kevin root certificate: %w", err)
		}
		sum := sha256.Sum256(pem)
		parts = append(parts, "#", "ca="+hex.EncodeToString(sum[:]))
	}
	return strings.Join(parts, " "), nil
}

// ensureHome creates the MINIKUBE_HOME of the cluster with its cache linked
// to the cache of the user's own minikube, which the clusters share.
func (d *minikubeDriver) ensureHome() error {
	cache, err := d.cacheDir()
	if err != nil {
		return err
	}
	if err = os.MkdirAll(cache, 0o750); err != nil {
		return fmt.Errorf("create the cache directory: %w", err)
	}
	if err = os.MkdirAll(d.home(), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", d.home(), err)
	}
	link := filepath.Join(d.home(), "cache")
	if _, err = os.Lstat(link); err == nil {
		return nil
	}
	if err = os.Symlink(cache, link); err != nil {
		return fmt.Errorf("link the cache: %w", err)
	}
	return nil
}

// prepareHome replaces the MINIKUBE_HOME of the cluster with a fresh one. A
// certificate in its certs directory is installed in every node before
// containerd starts.
func (d *minikubeDriver) prepareHome() error {
	if err := os.RemoveAll(filepath.Dir(d.home())); err != nil {
		return fmt.Errorf("remove the previous state: %w", err)
	}
	if err := d.ensureHome(); err != nil {
		return err
	}
	if !d.mountsCA() {
		return nil
	}
	pem, err := os.ReadFile(d.env.CAPath)
	if err != nil {
		return fmt.Errorf("read the kevin root certificate: %w", err)
	}
	certs := filepath.Join(d.home(), "certs")
	if err = os.MkdirAll(certs, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", certs, err)
	}
	//nolint:gosec // the path is under the workspace that kevin owns
	if err = os.WriteFile(filepath.Join(certs, "kevin-root.pem"), pem, 0o600); err != nil {
		return fmt.Errorf("write the kevin root certificate: %w", err)
	}
	return nil
}

// Create removes a stale cluster of the same name, then creates a fresh one.
// Unless retain is set, a failure removes what it made.
func (d *minikubeDriver) Create(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error) {
	if d.rt == nil {
		return nil, fmt.Errorf("kubernetes: minikube: create %q: %w", d.name, ErrNoRuntime)
	}

	// A stale cluster of this name survives a crash or a changed config;
	// Delete succeeds when there is none.
	if err := d.Delete(ctx, out); err != nil {
		return nil, fmt.Errorf("kubernetes: minikube: remove the previous cluster %q: %w", d.name, err)
	}
	if err := d.prepareHome(); err != nil {
		return nil, fmt.Errorf("kubernetes: minikube: prepare the state of %q: %w", d.name, err)
	}

	nodes, err := d.createNodes(ctx, spec, out)
	if err != nil {
		if !d.cfg.Retain {
			// A failed Up gets no Down. The error is reported below.
			_ = d.Delete(context.WithoutCancel(ctx), out)
		}
		return nil, err
	}

	out.Log("stdout", fmt.Sprintf("cluster %s is ready with %d node(s)", d.name, len(nodes)))
	return nodes, nil
}

// createNodes starts the cluster and points containerd in each node at the
// proxy.
func (d *minikubeDriver) createNodes(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error) {
	// minikube cannot parse a dual-stack docker network and falls back to the
	// default bridge, so create an IPv4-only one for it to use.
	if d.env.Engine != enginePodman {
		if err := d.rt.NetworkCreate(ctx, d.network(), cri.NetworkOptions{
			Labels: map[string]string{cri.LabelProject: d.env.Project},
		}); err != nil {
			return nil, fmt.Errorf("kubernetes: minikube: create the network of %q: %w", d.name, err)
		}
	}

	out.Log("stdout", "creating cluster "+d.name)
	out.Progress("creating "+d.name, 0, 0)
	start := d.startSpec(spec)
	if err := d.start(ctx, start, plugin.NewLineWriter(out, "stdout"), plugin.NewLineWriter(out, "stderr")); err != nil {
		return nil, fmt.Errorf("kubernetes: minikube: create the cluster %q: %w", d.name, err)
	}

	nodes, err := d.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("kubernetes: minikube: cluster %q: %w", d.name, ErrNoNodes)
	}

	if proxy := proxyEnv(d.cfg, d.env.ProxyEnv); len(proxy) > 0 {
		for _, node := range nodes {
			if err = d.setContainerdProxy(ctx, node, proxy); err != nil {
				return nil, err
			}
		}
	}
	return nodes, nil
}

// Delete removes the cluster, the workers that outlive it, and the network
// that its nodes joined.
func (d *minikubeDriver) Delete(ctx context.Context, out plugin.Emitter) error {
	// minikube needs a MINIKUBE_HOME to run in, and a crash can leave none.
	if err := d.ensureHome(); err != nil {
		return fmt.Errorf("kubernetes: minikube: prepare the state of %q: %w", d.name, err)
	}
	stderr := plugin.NewLineWriter(out, "stderr")
	if err := d.deleteProfile(ctx, d.name, d.home(), stderr); err != nil {
		return fmt.Errorf("kubernetes: minikube: delete the cluster %q: %w", d.name, err)
	}

	if d.rt != nil {
		// Without the profile state, minikube deletes only the container of the
		// name it is given, so each remaining node needs a delete of its own.
		leftover, err := d.Nodes(ctx)
		if err != nil {
			return err
		}
		for _, node := range leftover {
			if err = d.deleteProfile(ctx, node, d.home(), stderr); err != nil {
				return fmt.Errorf("kubernetes: minikube: delete the node %q: %w", node, err)
			}
		}
		// minikube leaves behind a network that it created itself.
		if err = d.rt.NetworkRemove(ctx, d.network()); err != nil {
			return fmt.Errorf("kubernetes: minikube: remove the network of %q: %w", d.name, err)
		}
	}

	if err := os.RemoveAll(filepath.Dir(d.home())); err != nil {
		return fmt.Errorf("kubernetes: minikube: remove the state of %q: %w", d.name, err)
	}
	return nil
}

// Context is the cluster name.
func (d *minikubeDriver) Context() string { return d.name }

// ControlPlane is the control-plane node. minikube names its container and
// its Kubernetes node after the cluster.
func (d *minikubeDriver) ControlPlane() string { return d.name }

// Kubectl runs kubectl inside the control-plane node.
func (d *minikubeDriver) Kubectl(ctx context.Context, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: minikube: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.Exec(ctx, d.ControlPlane(), minikubeKubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: minikube: kubectl: %w", err)
	}
	return out, nil
}

// KubectlInput runs kubectl inside the control-plane node, with stdin
// feeding the command.
func (d *minikubeDriver) KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: minikube: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.ExecInput(ctx, d.ControlPlane(), stdin, minikubeKubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: minikube: kubectl: %w", err)
	}
	return out, nil
}

// minikubeKubectlArgs wraps args in the shell command that finds kubectl in
// a node. The first argument after the script is the shell's own name.
func minikubeKubectlArgs(args []string) []string {
	full := make([]string, 0, len(args)+4)
	full = append(full, "sh", "-c", minikubeKubectlScript, "kubectl")
	return append(full, args...)
}

// LoadImage loads the image archive at path into the nodes.
func (d *minikubeDriver) LoadImage(ctx context.Context, path string, out plugin.Emitter) error {
	if err := d.loadImage(ctx, d.name, d.home(), path, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kubernetes: minikube: load the image archive: %w", err)
	}
	return nil
}
