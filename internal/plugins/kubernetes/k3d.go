package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/k3dcmd"
	"github.com/justenwalker/kevin/internal/podman"
	"github.com/justenwalker/kevin/plugin"
)

// k3sKubeconfig is the kubeconfig that a k3s node carries for its own
// cluster.
const k3sKubeconfig = "/etc/rancher/k3s/k3s.yaml"

// k3dCAPath is where Create mounts the kevin root certificate in every node.
// k3s reads every file in this directory as a trusted root when it starts.
const k3dCAPath = "/etc/ssl/certs/kevin-root.crt"

// k3sPodCIDR and k3sServiceCIDR are the pod and service subnets that Create
// sets on the server, and that ClusterCIDRs reports.
const (
	k3sPodCIDR     = "10.42.0.0/16"
	k3sServiceCIDR = "10.43.0.0/16"
)

// k3dDriver creates the cluster with the k3d command. k3d runs each node as
// a container, so a command in a node goes through the container runtime.
type k3dDriver struct {
	cfg        config
	env        plugin.Env
	name       string
	kubeconfig string
	rt         cri.Runtime

	// The k3dcmd calls that Create, Delete and the reads make, held as values
	// so that a test can stub them.
	create          func(ctx context.Context, spec k3dcmd.CreateSpec, stdout, stderr io.Writer) error
	deleteCluster   func(ctx context.Context, name string, env map[string]string, stderr io.Writer) error
	listNodes       func(ctx context.Context, name string, env map[string]string) ([]string, error)
	writeKubeconfig func(ctx context.Context, name, path string, env map[string]string) error
	importImage     func(ctx context.Context, spec k3dcmd.ImageImportSpec, stderr io.Writer) error
	freePort        func(ctx context.Context) (int, error)
	socket          func(ctx context.Context) (string, error)
}

var _ driver = (*k3dDriver)(nil)

// newK3dDriver returns the k3d driver for one cluster. It returns
// [ErrK3dWorkerSettings] when a worker carries node settings,
// [ErrK3dReservedEnv] when k3d.env sets a proxy variable that kevin sets, and
// [ErrK3dReservedLabel] when k3d.labels sets the kevin.node label. rt may be
// nil, and then Delete leaves the network of the cluster in place.
func newK3dDriver(cfg config, env plugin.Env, name, kubeconfig string, rt cri.Runtime) (*k3dDriver, error) {
	for worker, settings := range cfg.Workers {
		if len(settings) > 0 {
			return nil, fmt.Errorf("worker %q: %w", worker, ErrK3dWorkerSettings)
		}
	}
	for key := range cfg.K3d.Env {
		if _, reserved := proxyEnv(cfg, env.ProxyEnv)[key]; reserved {
			return nil, fmt.Errorf("env %q: %w", key, ErrK3dReservedEnv)
		}
	}
	if _, reserved := cfg.K3d.Labels[nodeLabelKey]; reserved {
		return nil, fmt.Errorf("labels %q: %w", nodeLabelKey, ErrK3dReservedLabel)
	}
	return &k3dDriver{
		cfg: cfg, env: env, name: name, kubeconfig: kubeconfig, rt: rt,
		create:          k3dcmd.Create,
		deleteCluster:   k3dcmd.Delete,
		listNodes:       k3dcmd.GetNodes,
		writeKubeconfig: k3dcmd.KubeconfigWrite,
		importImage:     k3dcmd.ImageImport,
		freePort:        freeLoopbackPort,
		socket:          podman.Socket,
	}, nil
}

// dockerEnv reports the DOCKER_HOST variable that points a k3d call at the
// podman service, or nothing under docker.
func (d *k3dDriver) dockerEnv(ctx context.Context) (map[string]string, error) {
	if d.env.Engine != enginePodman {
		return map[string]string{}, nil
	}
	socket, err := d.socket(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: %w", err)
	}
	return map[string]string{"DOCKER_HOST": "unix://" + socket}, nil
}

// network is the name of the docker network that the nodes of the cluster
// join. The prefix keeps Delete from removing a network of another name.
func (d *k3dDriver) network() string { return "kevin-k3d-" + d.name }

// workers lists the worker names in node creation order.
func (d *k3dDriver) workers() []string { return slices.Sorted(maps.Keys(d.cfg.Workers)) }

// mountsCA reports whether Create mounts the kevin root certificate into the
// nodes.
func (d *k3dDriver) mountsCA() bool { return wantsTrustCA(d.cfg, d.env) }

// createSpec builds the k3d cluster create arguments for the cluster. The API
// port is left for Create to choose, because a fresh port would keep
// Fingerprint from ever matching.
func (d *k3dDriver) createSpec(spec createSpec) k3dcmd.CreateSpec {
	create := k3dcmd.CreateSpec{
		Name:       d.name,
		Network:    d.network(),
		Image:      d.cfg.K3d.Image,
		NoRollback: d.cfg.Retain,
		Agents:     len(d.cfg.Workers),
		Wait:       spec.Wait,
		Memory:     d.cfg.K3d.Memory,
		Env:        mergeEnv(d.cfg.K3d.Env, proxyEnv(d.cfg, d.env.ProxyEnv)),
		Ports:      k3dPortFlags(spec.Ports),
		NodeLabels: []string{nodeLabelKey + "=" + controlPlaneNodeName + "@server:0"},
		K3sArgs: []string{
			"--cluster-cidr=" + k3sPodCIDR + "@server:*",
			"--service-cidr=" + k3sServiceCIDR + "@server:*",
		},
	}
	for _, component := range slices.Compact(slices.Sorted(slices.Values(d.cfg.K3d.Disable))) {
		create.K3sArgs = append(create.K3sArgs, "--disable="+component+"@server:*")
	}
	for _, key := range slices.Sorted(maps.Keys(d.cfg.K3d.Labels)) {
		create.NodeLabels = append(create.NodeLabels, key+"="+d.cfg.K3d.Labels[key]+"@server:*;agent:*")
	}
	if d.mountsCA() {
		create.Volumes = []string{d.env.CAPath + ":" + k3dCAPath + "@server:*;agent:*"}
	}
	for _, m := range resolveMounts(d.cfg.Mounts, d.env.ProjectDir) {
		volume := m.Host + ":" + m.Container
		if m.ReadOnly {
			volume += ":ro"
		}
		create.Volumes = append(create.Volumes, volume+"@server:*;agent:*")
	}
	for i, worker := range d.workers() {
		create.NodeLabels = append(create.NodeLabels, fmt.Sprintf("%s=%s@agent:%d", nodeLabelKey, worker, i))
	}
	return create
}

// k3dPortFlags renders ports as k3d cluster create --port values on the
// loopback interface: one "127.0.0.1:hostPort:1080/tcp" entry for the SOCKS5
// gateway, one "127.0.0.1:hostPort:40000+i/udp" per reserved UDP ASSOCIATE
// pool port, each for the control-plane node.
func k3dPortFlags(ports clusterrelay.Ports) []string {
	if ports.TCP == 0 {
		return nil
	}
	flags := []string{fmt.Sprintf("127.0.0.1:%d:%d/tcp@server:0", ports.TCP, clusterrelay.NodePort)}
	for i, hostPort := range ports.UDP {
		flags = append(flags, fmt.Sprintf("127.0.0.1:%d:%d/udp@server:0", hostPort, clusterrelay.UDPNodePortBase+i))
	}
	return flags
}

// freeLoopbackPort asks the OS for a free TCP port on the loopback interface.
func freeLoopbackPort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen: %w", err)
	}
	addr, ok := l.Addr().(*net.TCPAddr)
	if err = l.Close(); err != nil {
		return 0, fmt.Errorf("release the port: %w", err)
	}
	if !ok {
		return 0, ErrNotTCPAddr
	}
	return addr.Port, nil
}

// Nodes lists the node containers of the cluster: the server, then the
// agents.
func (d *k3dDriver) Nodes(ctx context.Context) ([]string, error) {
	env, err := d.dockerEnv(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := d.listNodes(ctx, d.name, env)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: list the nodes of %q: %w", d.name, err)
	}
	return nodes, nil
}

// Fingerprint reports the exact k3d cluster create arguments the cluster runs
// with, including the resolved proxy endpoint and the content of the kevin
// root certificate, as a single comparable string. k3d bakes both into the
// nodes once, at creation, and nothing updates them afterward.
func (d *k3dDriver) Fingerprint(spec createSpec) (string, error) {
	fingerprint := strings.Join(k3dcmd.CreateArgs(d.createSpec(spec)), " ")
	if !d.mountsCA() {
		return fingerprint, nil
	}
	pem, err := os.ReadFile(d.env.CAPath)
	if err != nil {
		return "", fmt.Errorf("kubernetes: k3d: read the kevin root certificate: %w", err)
	}
	sum := sha256.Sum256(pem)
	return fingerprint + " # ca=" + hex.EncodeToString(sum[:]), nil
}

// Create removes a stale cluster of the same name, then creates a fresh one
// and writes its kubeconfig. Unless retain is set, a failure removes what it
// made.
func (d *k3dDriver) Create(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error) {
	if d.rt == nil {
		return nil, fmt.Errorf("kubernetes: k3d: create the network of %q: %w", d.name, ErrNoRuntime)
	}

	// A stale cluster of this name survives a crash or a changed config;
	// Delete succeeds when there is none.
	if err := d.Delete(ctx, out); err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: remove the previous cluster %q: %w", d.name, err)
	}

	// Docker can default to dual-stack networks. Create an IPv4-only network
	// for the nodes, and let Delete remove it.
	if err := d.rt.NetworkCreate(ctx, d.network(), cri.NetworkOptions{
		Labels: map[string]string{cri.LabelProject: d.env.Project},
	}); err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: create the network of %q: %w", d.name, err)
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

// createNodes starts the cluster and writes its kubeconfig.
func (d *k3dDriver) createNodes(ctx context.Context, spec createSpec, out plugin.Emitter) ([]string, error) {
	out.Log("stdout", "creating cluster "+d.name)
	out.Progress("creating "+d.name, 0, 0)

	// Without --api-port, k3d publishes the API server on every interface.
	create := d.createSpec(spec)
	port, err := d.freePort(ctx)
	if err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: pick a port for the API server of %q: %w", d.name, err)
	}
	create.APIPort = port
	if create.CommandEnv, err = d.dockerEnv(ctx); err != nil {
		return nil, err
	}

	if err = d.create(ctx, create, plugin.NewLineWriter(out, "stdout"), plugin.NewLineWriter(out, "stderr")); err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: create the cluster %q: %w", d.name, err)
	}
	if err = d.writeKubeconfig(ctx, d.name, d.kubeconfig, create.CommandEnv); err != nil {
		return nil, fmt.Errorf("kubernetes: k3d: write the kubeconfig of %q: %w", d.name, err)
	}

	nodes, err := d.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("kubernetes: k3d: cluster %q: %w", d.name, ErrNoNodes)
	}
	return nodes, nil
}

// Delete removes the cluster, and the network that Create made for it.
func (d *k3dDriver) Delete(ctx context.Context, out plugin.Emitter) error {
	env, err := d.dockerEnv(ctx)
	if err != nil {
		return err
	}
	if err = d.deleteCluster(ctx, d.name, env, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kubernetes: k3d: delete the cluster %q: %w", d.name, err)
	}
	if d.rt == nil {
		return nil
	}
	if err := d.rt.NetworkRemove(ctx, d.network()); err != nil {
		return fmt.Errorf("kubernetes: k3d: remove the network of %q: %w", d.name, err)
	}
	return nil
}

// Context is "k3d-" and the cluster name.
func (d *k3dDriver) Context() string { return "k3d-" + d.name }

// ControlPlane is the server node. k3d names it "k3d-<cluster>-server-0", and
// k3s registers the node under the same name.
func (d *k3dDriver) ControlPlane() string { return "k3d-" + d.name + "-server-0" }

// ClusterCIDRs reports the pod and service subnets of the cluster: the k3s
// defaults, since kevin sets neither.
func (*k3dDriver) ClusterCIDRs() []string { return []string{k3sPodCIDR, k3sServiceCIDR} }

// CoreDNSCustom marks the cluster as one that reverts an edit to the coredns
// configmap: k3s applies its own copy again, and imports extra zones from
// the coredns-custom configmap.
func (*k3dDriver) CoreDNSCustom() {}

// RefreshAccess does nothing: k3d publishes the API server on a host port
// that kevin chose, which joining a network does not change.
func (*k3dDriver) RefreshAccess(context.Context) error { return nil }

// LabelNodes does nothing: k3d sets the labels at creation.
func (*k3dDriver) LabelNodes(context.Context) error { return nil }

// Kubectl runs kubectl inside the server node.
func (d *k3dDriver) Kubectl(ctx context.Context, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: k3d: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.Exec(ctx, d.ControlPlane(), k3dKubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: k3d: kubectl: %w", err)
	}
	return out, nil
}

// KubectlInput runs kubectl inside the server node, with stdin feeding the
// command.
func (d *k3dDriver) KubectlInput(ctx context.Context, stdin io.Reader, args ...string) (string, error) {
	if d.rt == nil {
		return "", fmt.Errorf("kubernetes: k3d: kubectl: %w", ErrNoRuntime)
	}
	out, err := d.rt.ExecInput(ctx, d.ControlPlane(), stdin, k3dKubectlArgs(args)...)
	if err != nil {
		return "", fmt.Errorf("kubernetes: k3d: kubectl: %w", err)
	}
	return out, nil
}

// k3dKubectlArgs prepends the kubectl command and the k3s kubeconfig flag to
// args.
func k3dKubectlArgs(args []string) []string {
	full := make([]string, 0, len(args)+3)
	full = append(full, "kubectl", "--kubeconfig", k3sKubeconfig)
	return append(full, args...)
}

// LoadImage loads the image archive at path into the nodes.
func (d *k3dDriver) LoadImage(ctx context.Context, path string, out plugin.Emitter) error {
	env, err := d.dockerEnv(ctx)
	if err != nil {
		return err
	}
	spec := k3dcmd.ImageImportSpec{Name: d.name, Path: path, CommandEnv: env}
	if err = d.importImage(ctx, spec, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kubernetes: k3d: load the image archive: %w", err)
	}
	return nil
}
