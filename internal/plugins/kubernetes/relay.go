package kubernetes

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

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/plugin"
)

// wantsRelay reports whether Up must stand up the SOCKS5 relay.
func wantsRelay(cfg config) bool { return cfg.Relay || len(cfg.Expose) > 0 }

// relayAddrFile is where Up persists the relay's host:port, alongside
// kubeconfig.
func relayAddrFile(kubeconfig string) string { return kubeconfig + ".relay-addr" }

// readRelayPort reads back the host port that a previous Up picked for the
// relay, from the relay address Up persists at relayAddrFile. It reports ok
// false when no relay was set up last time, or the file does not parse -
// either way, the caller must treat that as "no reusable port", not an
// error: a node's port mappings are fixed at cluster creation, so a
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

// reusableRelayPorts reads back the relay ports a previous Up reserved for
// kubeconfig, when useRelay - reusablePorts is false when useRelay is true
// but either the TCP port or the UDP pool can't be read back, meaning the
// caller must pick fresh ones and cannot reuse an existing cluster as-is.
func reusableRelayPorts(kubeconfig string, useRelay bool) (clusterrelay.Ports, bool) {
	if !useRelay {
		return clusterrelay.Ports{}, true
	}
	tcp, tcpOK := readRelayPort(kubeconfig)
	udp, udpOK := readRelayUDPPorts(kubeconfig)
	return clusterrelay.Ports{TCP: tcp, UDP: udp}, tcpOK && udpOK
}

// persistRelayPorts writes relayAddress and udpHostPorts to their marker
// files when useRelay, or removes any stale ones from a previous Up
// otherwise - readRelayPort/readRelayUDPPorts's counterpart, called once
// Up knows the final result.
func persistRelayPorts(kubeconfig, name, relayAddress string, udpHostPorts []int, useRelay bool) error {
	if !useRelay {
		if err := os.Remove(relayAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("kubernetes: remove the stale relay address for %q: %w", name, err)
		}
		if err := os.Remove(relayUDPAddrFile(kubeconfig)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("kubernetes: remove the stale relay udp pool for %q: %w", name, err)
		}
		return nil
	}
	if err := os.WriteFile(relayAddrFile(kubeconfig), []byte(relayAddress), 0o600); err != nil {
		return fmt.Errorf("kubernetes: write the relay address for %q: %w", name, err)
	}
	if err := writeRelayUDPPorts(kubeconfig, udpHostPorts); err != nil {
		return fmt.Errorf("kubernetes: write the relay udp pool for %q: %w", name, err)
	}
	return nil
}

// finishRelay deploys the SOCKS5 relay pod and reports each expose entry as
// a routed endpoint, once the cluster is up. Callers must only call this
// when wantsRelay(cfg) holds.
func finishRelay(ctx context.Context, rt cri.Runtime, drv driver, cfg config, relayAddress string, udpHostPorts []int, out plugin.Emitter) ([]plugin.ExposedPort, error) {
	// The kevin.cue-configured relay image (cfg.Relay.Image) lives at the
	// supervisor level, not in plugin.Env - relay.Ref("") is exactly what
	// the integration suite already uses to resolve the same image for the
	// same reason: KEVIN_RELAY_IMAGE wins, else the built-in tag.
	if err := deployRelay(ctx, rt, drv, relay.Ref(""), len(udpHostPorts), out); err != nil {
		return nil, err
	}
	return exposedViaRelay(cfg.Expose, relayAddress, clusterrelay.UDPAddrs(udpHostPorts))
}

// saveImageToTempFile saves image to a temporary tar file and returns its
// path - the cluster tools load an image archive from a file path, not
// stdin, unlike every other command they run.
func saveImageToTempFile(ctx context.Context, rt cri.Runtime, image string) (string, error) {
	src, err := rt.Save(ctx, image)
	if err != nil {
		return "", fmt.Errorf("kubernetes: save the relay image: %w", err)
	}
	defer func() { _ = src.Close() }()

	f, err := os.CreateTemp("", "kevin-relay-*.tar")
	if err != nil {
		return "", fmt.Errorf("kubernetes: create a temp file for the relay image: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err = io.Copy(f, src); err != nil {
		return "", fmt.Errorf("kubernetes: write the relay image to %s: %w", f.Name(), err)
	}
	return f.Name(), nil
}

// deployRelay loads the kevin-relay image into the cluster and applies a
// Pod running it in SOCKS5 mode, pinned to the control-plane node - mirrors
// patchCoreDNS's shape: act on the cluster via kubectl, wait for it to be
// ready.
func deployRelay(ctx context.Context, rt cri.Runtime, drv driver, image string, udpPoolSize int, out plugin.Emitter) error {
	out.Log("stdout", "loading the relay image")
	out.Progress("loading the relay image", 0, 0)
	tarPath, err := saveImageToTempFile(ctx, rt, image)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tarPath) }()
	if err = drv.LoadImage(ctx, tarPath, out); err != nil {
		return fmt.Errorf("kubernetes: load the relay image: %w", err)
	}

	out.Log("stdout", "starting the socks5 relay")
	out.Progress("starting the socks5 relay", 0, 0)
	manifest := clusterrelay.PodManifest(drv.ControlPlane(), image, udpPoolSize)
	if _, err = drv.KubectlInput(ctx, strings.NewReader(manifest),
		"-n", "kube-system", "apply", "-f", "-"); err != nil {
		return fmt.Errorf("kubernetes: apply the relay pod: %w", err)
	}
	if _, err = drv.Kubectl(ctx, "-n", "kube-system", "wait", "pod/kevin-relay",
		"--for=condition=Ready", "--timeout=60s"); err != nil {
		return fmt.Errorf("kubernetes: wait for the relay pod: %w", err)
	}

	out.Log("stdout", "socks5 relay ready")
	return nil
}

// exposedViaRelay reports each expose entry as a relay-routed endpoint,
// sorted by name for a stable result despite Go's randomized map
// iteration. A plain host:port isn't enough information here - a client
// must dial the relay and ask it to reach the real target through a SOCKS5
// command (CONNECT for tcp, ASSOCIATE for udp) - so Upstream carries both,
// as a single socks5://<relay>/<target> string. udpAddrs is the relay's
// published UDP pool, required (ErrNoRelayUDPPool otherwise) by any udp
// entry.
func exposedViaRelay(entries map[string]expose, addr string, udpAddrs map[string]string) ([]plugin.ExposedPort, error) {
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
				return nil, fmt.Errorf("kubernetes: expose %s: %w", name, ErrNoRelayUDPPool)
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
