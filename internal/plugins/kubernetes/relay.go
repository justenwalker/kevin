package kubernetes

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/justenwalker/kevin/internal/clusterrelay"
	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
	"github.com/justenwalker/kevin/plugin"
)

// wantsRelay reports whether Up must stand up the SOCKS5 relay.
func wantsRelay(cfg config) bool { return cfg.Relay || len(cfg.Expose) > 0 }

// relayResult is what finishRelay adds to the result of Up.
type relayResult struct {
	Exposed []plugin.ExposedPort
	Addr    string
}

// finishRelay deploys the SOCKS5 relay pod, starts the forwarder container
// that publishes its ports on the host, and reports each expose entry as a
// routed endpoint, once the cluster is up. fwd names the forwarder; its
// Target is the control-plane node. Callers must only call this when
// wantsRelay(cfg) holds.
func finishRelay(ctx context.Context, rt cri.Runtime, drv driver, cfg config, fwd clusterrelay.ForwarderSpec, out plugin.Emitter) (relayResult, error) {
	poolSize, err := relay.UDPPoolSize()
	if err != nil {
		return relayResult{}, fmt.Errorf("kubernetes: %w", err)
	}
	// The kevin.cue-configured relay image (cfg.Relay.Image) lives at the
	// supervisor level, not in plugin.Env - relay.Ref("") is exactly what
	// the integration suite already uses to resolve the same image for the
	// same reason: KEVIN_RELAY_IMAGE wins, else the built-in tag.
	fwd.Image = relay.Ref("")
	if err = deployRelay(ctx, rt, drv, fwd.Image, poolSize, out); err != nil {
		return relayResult{}, err
	}

	fwd.Target = drv.ControlPlane()
	out.Log("stdout", "starting the relay forwarder")
	forwarder, err := clusterrelay.StartForwarder(ctx, rt, fwd)
	if err != nil {
		return relayResult{}, fmt.Errorf("kubernetes: %w", err)
	}
	exposed, err := exposedViaRelay(cfg.Expose, forwarder.Addr, forwarder.UDPAddrs)
	if err != nil {
		return relayResult{}, err
	}
	return relayResult{Exposed: exposed, Addr: forwarder.Addr}, nil
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
