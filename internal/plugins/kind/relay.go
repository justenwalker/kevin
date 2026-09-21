package kind

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/plugin"
)

// relayNodePort is the fixed port the SOCKS5 relay pod listens on for its
// TCP CONNECT gateway.
const relayNodePort = 1080

// relayUDPNodePortBase is the first node-internal port in the relay pod's
// fixed UDP ASSOCIATE pool - the Kubernetes hostPort/containerPort baked
// into both the Pod manifest and the control-plane node's
// extraPortMappings, distinct from relayNodePort's own TCP gateway.
const relayUDPNodePortBase = 40000

// relayPorts is the set of host ports reserved for the SOCKS5 relay pod:
// one fixed TCP port, plus a UDP ASSOCIATE pool sized by
// relay.UDPPoolSize(). Bundled into one struct (ADR-0006) instead of a
// second bare parameter threaded alongside TCP everywhere this already
// goes.
type relayPorts struct {
	// TCP is the host port for the relay's SOCKS5 gateway. Zero means no
	// relay is wanted at all.
	TCP int

	// UDP is the host ports reserved for the relay's UDP ASSOCIATE pool,
	// one per pool port, in order starting at relayUDPNodePortBase. Empty
	// when the pool is disabled (KEVIN_RELAY_UDP_POOL_SIZE=0).
	UDP []int
}

// wantsRelay reports whether Up must stand up the SOCKS5 relay.
func wantsRelay(cfg config) bool { return cfg.Relay || len(cfg.Expose) > 0 }

// relayAddr is the loopback address the relay's SOCKS5 port is published
// on, for a given host port.
func relayAddr(hostPort int) string {
	return fmt.Sprintf("127.0.0.1:%d", hostPort)
}

// relayUDPAddrs maps each fixed node-internal UDP pool port to its
// freshly-reserved host address, the shape ExposedPort.RelayUDPAddrs
// expects - the same information internal/relay's own SOCKS5UDPAddrs()
// reports, built here instead of read back from a running container since
// kind's ports are host ports chosen ahead of cluster creation, not
// container ports discovered after the fact.
func relayUDPAddrs(hostPorts []int) map[string]string {
	if len(hostPorts) == 0 {
		return nil
	}
	addrs := make(map[string]string, len(hostPorts))
	for i, hostPort := range hostPorts {
		addrs[strconv.Itoa(relayUDPNodePortBase+i)] = relayAddr(hostPort)
	}
	return addrs
}

// finishRelay deploys the SOCKS5 relay pod and reports each expose entry as
// a routed endpoint, once the cluster is up. Callers must only call this
// when wantsRelay(cfg) holds.
func finishRelay(ctx context.Context, rt cri.Runtime, cfg config, name string, allNodes []string, relayAddress string, udpHostPorts []int, env plugin.Env, out plugin.Emitter) ([]plugin.ExposedPort, error) {
	// The kevin.cue-configured relay image (cfg.Relay.Image) lives at the
	// supervisor level, not in plugin.Env - relay.Ref("") is exactly what
	// the kind integration suite already uses to resolve the same image for
	// the same reason: KEVIN_RELAY_IMAGE wins, else the built-in tag.
	if err := deployRelay(ctx, rt, name, allNodes, relay.Ref(""), len(udpHostPorts), env, out); err != nil {
		return nil, err
	}
	return exposedViaRelay(cfg.Expose, relayAddress, relayUDPAddrs(udpHostPorts))
}

// findFreePort asks the OS for a free port on the host.
// There's an unavoidable race between closing this listener and
// kind binding the same port for real.
func findFreePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("kind: pick a free port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("kind: unexpected listener address type %T", ln.Addr())
	}
	return addr.Port, nil
}

// findFreePorts asks the OS for n free ports on the host, held open
// simultaneously so the OS can't hand two of them the same number, then
// closed together right before returning - the same unavoidable race
// findFreePort accepts for one port, not made worse for n.
func findFreePorts(ctx context.Context, n int) ([]int, error) {
	var lc net.ListenConfig
	lns := make([]net.Listener, 0, n)
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()

	ports := make([]int, n)
	for i := range ports {
		ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("kind: pick a free port: %w", err)
		}
		lns = append(lns, ln)
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("kind: unexpected listener address type %T", ln.Addr())
		}
		ports[i] = addr.Port
	}
	return ports, nil
}

// saveImageToTempFile docker-saves image to a temporary tar file and returns
// its path - kind load image-archive takes a file path, not stdin, unlike
// every other command this plugin shells out to.
func saveImageToTempFile(ctx context.Context, rt cri.Runtime, image string) (string, error) {
	src, err := rt.Save(ctx, image)
	if err != nil {
		return "", fmt.Errorf("kind: save the relay image: %w", err)
	}
	defer func() { _ = src.Close() }()

	f, err := os.CreateTemp("", "kevin-kind-relay-*.tar")
	if err != nil {
		return "", fmt.Errorf("kind: create a temp file for the relay image: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err = io.Copy(f, src); err != nil {
		return "", fmt.Errorf("kind: write the relay image to %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}

// deployRelay loads the kevin-relay image into the control-plane node and
// applies a Pod running it in SOCKS5 mode, pinned to that same node -
// mirrors patchCoreDNS's shape: find the node, act on it via
// kubectl-through-docker-exec, wait for it to be ready.
func deployRelay(ctx context.Context, rt cri.Runtime, name string, allNodes []string, image string, udpPoolSize int, env plugin.Env, out plugin.Emitter) error {
	container, err := bootstrapControlPlaneNode(allNodes)
	if err != nil {
		return fmt.Errorf("kind: find the control plane node: %w", err)
	}

	out.Log("stdout", "loading the relay image")
	out.Progress("loading the relay image", 0, 0)
	tarPath, err := saveImageToTempFile(ctx, rt, image)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarPath) }()
	if err = kindcmd.LoadImageArchive(ctx, kindcmd.LoadImageArchiveSpec{Name: name, Path: tarPath, Env: providerEnv(env)}, plugin.NewLineWriter(out, "stderr")); err != nil {
		return fmt.Errorf("kind: load the relay image: %w", err)
	}

	out.Log("stdout", "starting the socks5 relay")
	out.Progress("starting the socks5 relay", 0, 0)
	// The docker container name of a kind node is also its Kubernetes node
	// name - coredns.go's patch already relies on the same identity to find
	// this same node from inside the cluster.
	manifest := relayPodManifest(container, image, udpPoolSize)
	if _, err = kubectlInput(ctx, rt, container, strings.NewReader(manifest),
		"-n", "kube-system", "apply", "-f", "-"); err != nil {
		return fmt.Errorf("kind: apply the relay pod: %w", err)
	}
	if _, err = kubectl(ctx, rt, container, "-n", "kube-system", "wait", "pod/kevin-relay",
		"--for=condition=Ready", "--timeout=60s"); err != nil {
		return fmt.Errorf("kind: wait for the relay pod: %w", err)
	}

	out.Log("stdout", "socks5 relay ready")
	return nil
}

// relayPodManifest is a Pod spec running kevin-relay in SOCKS5 mode, pinned
// to nodeName so it lands on the node extraPortMappings targets.
// udpPoolSize is the number of UDP ASSOCIATE pool ports to bind, starting
// at relayUDPNodePortBase - zero adds neither a --udp-relay-ports arg nor
// any UDP ports entry, the same "no UDP capacity" default kevin-relay
// itself falls back to. imagePullPolicy is Never - the image is loaded
// locally, never pulled.
func relayPodManifest(nodeName, image string, udpPoolSize int) string {
	args := fmt.Sprintf(`"socks5-gateway", "--listen", ":%d"`, relayNodePort)
	var udpPorts strings.Builder
	if udpPoolSize > 0 {
		args += fmt.Sprintf(`, "--udp-relay-ports", "%d-%d"`, relayUDPNodePortBase, relayUDPNodePortBase+udpPoolSize-1)
		for i := range udpPoolSize {
			fmt.Fprintf(&udpPorts, "\n    - containerPort: %d\n      hostPort: %d\n      protocol: UDP",
				relayUDPNodePortBase+i, relayUDPNodePortBase+i)
		}
	}

	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: kevin-relay
  namespace: kube-system
spec:
  nodeName: %s
  containers:
  - name: kevin-relay
    image: %s
    imagePullPolicy: Never
    args: [%s]
    ports:
    - containerPort: %d
      hostPort: %d
      protocol: TCP%s
`, nodeName, image, args, relayNodePort, relayNodePort, udpPorts.String())
}

// exposedViaRelay reports each expose entry as a relay-routed endpoint,
// sorted by name for a stable result despite Go's randomized map
// iteration. A plain host:port isn't enough information here - a client
// must dial the relay and ask it to reach the real target through a SOCKS5
// command (CONNECT for tcp, ASSOCIATE for udp) - so Upstream carries both,
// as a single socks5://<relay>/<target> string. udpAddrs is the relay's
// published UDP pool, required (ErrNoRelayUDPPool otherwise) by any udp
// entry.
func exposedViaRelay(entries map[string]kindExpose, addr string, udpAddrs map[string]string) ([]plugin.ExposedPort, error) {
	out := make([]plugin.ExposedPort, 0, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		e := entries[name]
		ep := plugin.ExposedPort{
			Name:     name,
			Protocol: e.Protocol,
			Relay:    true,
			Upstream: fmt.Sprintf("socks5://%s/%s", addr, e.Address),
			HostPort: e.HostPort,
		}
		if e.Protocol == "udp" {
			if len(udpAddrs) == 0 {
				return nil, fmt.Errorf("kind: expose %s: %w", name, ErrNoRelayUDPPool)
			}
			ep.RelayUDPAddrs = udpAddrs
		}
		out = append(out, ep)
	}
	return out, nil
}

// exposedPortDetails mirrors every exposed port onto the step's card.
func exposedPortDetails(exposed []plugin.ExposedPort) []plugin.Detail {
	details := make([]plugin.Detail, 0, len(exposed))
	for _, ep := range exposed {
		details = append(details, ep.Detail())
	}
	return details
}
