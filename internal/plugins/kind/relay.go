package kind

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/internal/kindcmd"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/plugin"
)

// relayNodePort is the fixed port the SOCKS5 relay pod listens on.
const relayNodePort = 1080

// wantsRelay reports whether Up must stand up the SOCKS5 relay.
func wantsRelay(cfg config) bool { return cfg.Relay || len(cfg.Expose) > 0 }

// relayAddr is the loopback address the relay's SOCKS5 port is published
// on, for a given host port.
func relayAddr(hostPort int) string {
	return fmt.Sprintf("127.0.0.1:%d", hostPort)
}

// finishRelay deploys the SOCKS5 relay pod and reports each expose entry as
// a routed endpoint, once the cluster is up. Callers must only call this
// when wantsRelay(cfg) holds.
func finishRelay(ctx context.Context, cfg config, name string, allNodes []string, relayAddress string, out plugin.Emitter) ([]plugin.ExposedPort, error) {
	// The kevin.cue-configured relay image (cfg.Relay.Image) lives at the
	// supervisor level, not in plugin.Env - relay.Ref("") is exactly what
	// the kind integration suite already uses to resolve the same image for
	// the same reason: KEVIN_RELAY_IMAGE wins, else the built-in tag.
	if err := deployRelay(ctx, name, allNodes, relay.Ref(""), out); err != nil {
		return nil, err
	}
	return exposedViaRelay(cfg.Expose, relayAddress), nil
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

// saveImageToTempFile docker-saves image to a temporary tar file and returns
// its path - kind load image-archive takes a file path, not stdin, unlike
// every other command this plugin shells out to.
func saveImageToTempFile(ctx context.Context, image string) (string, error) {
	src, err := dockerClient.Save(ctx, image)
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
func deployRelay(ctx context.Context, name string, allNodes []string, image string, out plugin.Emitter) error {
	container, err := bootstrapControlPlaneNode(allNodes)
	if err != nil {
		return fmt.Errorf("kind: find the control plane node: %w", err)
	}

	out.Log("stdout", "loading the relay image")
	out.Progress("loading the relay image", 0, 0)
	tarPath, err := saveImageToTempFile(ctx, image)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarPath) }()
	if err = kindcmd.LoadImageArchive(ctx, kindcmd.LoadImageArchiveSpec{Name: name, Path: tarPath}, stderrWriter(out)); err != nil {
		return fmt.Errorf("kind: load the relay image: %w", err)
	}

	out.Log("stdout", "starting the socks5 relay")
	out.Progress("starting the socks5 relay", 0, 0)
	// The docker container name of a kind node is also its Kubernetes node
	// name - coredns.go's patch already relies on the same identity to find
	// this same node from inside the cluster.
	manifest := relayPodManifest(container, image)
	if _, err = kubectlInput(ctx, container, strings.NewReader(manifest),
		"-n", "kube-system", "apply", "-f", "-"); err != nil {
		return fmt.Errorf("kind: apply the relay pod: %w", err)
	}
	if _, err = kubectl(ctx, container, "-n", "kube-system", "wait", "pod/kevin-relay",
		"--for=condition=Ready", "--timeout=60s"); err != nil {
		return fmt.Errorf("kind: wait for the relay pod: %w", err)
	}

	out.Log("stdout", "socks5 relay ready")
	return nil
}

// relayPodManifest is a Pod spec running kevin-relay in SOCKS5 mode, pinned
// to nodeName so it lands on the node extraPortMappings targets.
// imagePullPolicy is Never - the image is loaded locally, never pulled.
func relayPodManifest(nodeName, image string) string {
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
    args: ["socks5-gateway", "--listen", ":%d"]
    ports:
    - containerPort: %d
      hostPort: %d
      protocol: TCP
`, nodeName, image, relayNodePort, relayNodePort, relayNodePort)
}

// exposedViaRelay reports each expose entry as a SOCKS5-routed endpoint,
// sorted by name for a stable result despite Go's randomized map
// iteration. A plain host:port isn't enough information here - a client
// must dial the relay and ask it to CONNECT to the real target - so
// Upstream carries both, as a single socks5://<relay>/<target> string.
func exposedViaRelay(entries map[string]kindExpose, addr string) []plugin.ExposedPort {
	out := make([]plugin.ExposedPort, 0, len(entries))
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		e := entries[name]
		out = append(out, plugin.ExposedPort{
			Name:     name,
			Protocol: "socks5",
			Upstream: fmt.Sprintf("socks5://%s/%s", addr, e.Address),
			HostPort: e.HostPort,
		})
	}
	return out
}

// exposedPortDetails mirrors every exposed port onto the step's card.
func exposedPortDetails(exposed []plugin.ExposedPort) []plugin.Detail {
	details := make([]plugin.Detail, 0, len(exposed))
	for _, ep := range exposed {
		details = append(details, ep.Detail())
	}
	return details
}
