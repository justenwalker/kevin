// Package clusterrelay builds the SOCKS5 relay pod manifest and runs the
// forwarder container that publishes the pod's ports on the host, shared by
// every Kubernetes-cluster step type.
package clusterrelay

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
	"github.com/justenwalker/kevin/internal/relay"
)

// NodePort is the fixed port the SOCKS5 relay pod listens on for its TCP
// CONNECT gateway.
const NodePort = 1080

// UDPNodePortBase is the first node-internal port of the relay pod's UDP
// ASSOCIATE pool.
const UDPNodePortBase = 40000

// Role marks the forwarder container as a cluster relay forwarder.
const Role = "cluster-relay-forwarder"

// targetLabel and poolLabel record the Target and UDP pool size a
// forwarder was started with.
const (
	targetLabel = "kevin.relay.target"
	poolLabel   = "kevin.relay.udp-pool"
)

// Forwarder is a running forwarder container's host-reachable relay ports.
type Forwarder struct {
	// Addr is the host address of the relay's SOCKS5 gateway.
	Addr string

	// UDPAddrs maps each node-internal UDP pool port to its host address,
	// the shape plugin.ExposedPort.RelayUDPAddrs expects. Nil when the pool
	// is disabled.
	UDPAddrs map[string]string
}

// ForwarderSpec describes the forwarder container for one cluster.
type ForwarderSpec struct {
	// Name is the container name, see [ForwarderName].
	Name string

	// Image is the kevin-relay image the container runs.
	Image string

	// Network is the project network the container shares with Target.
	Network string

	// Target is the control-plane node container the relay ports are
	// forwarded to.
	Target string

	// Project, Scope, and Step label the container as owned by the step.
	Project string
	Scope   string
	Step    string
}

// ForwarderName is the forwarder container's name for cluster.
func ForwarderName(cluster string) string {
	return "kevin-" + cluster + "-relay-fwd"
}

// StartForwarder runs the forwarder container for spec and reports its host
// addresses. It reuses a running container that was started for the same
// Target and UDP pool size, and replaces any other one under the same name.
func StartForwarder(ctx context.Context, rt cri.Runtime, spec ForwarderSpec) (Forwarder, error) {
	poolSize, err := relay.UDPPoolSize()
	if err != nil {
		return Forwarder{}, fmt.Errorf("clusterrelay: %w", err)
	}
	pool := strconv.Itoa(poolSize)

	info, err := rt.Inspect(ctx, spec.Name)
	switch {
	case err == nil && info.Running && info.Labels[targetLabel] == spec.Target && info.Labels[poolLabel] == pool:
		return forwarderFromInfo(info)
	case err != nil && !errors.Is(err, cri.ErrNotFound):
		return Forwarder{}, fmt.Errorf("clusterrelay: inspect %s: %w", spec.Name, err)
	}

	if err = rt.Remove(ctx, spec.Name); err != nil {
		return Forwarder{}, fmt.Errorf("clusterrelay: remove %s: %w", spec.Name, err)
	}
	ports := []string{fmt.Sprintf("127.0.0.1::%d", NodePort)}
	cmd := []string{"port-forward", "--target", spec.Target, "--tcp", strconv.Itoa(NodePort)}
	if poolSize > 0 {
		cmd = append(cmd, "--udp-relay-ports", fmt.Sprintf("%d-%d", UDPNodePortBase, UDPNodePortBase+poolSize-1))
		for i := range poolSize {
			ports = append(ports, fmt.Sprintf("127.0.0.1::%d/udp", UDPNodePortBase+i))
		}
	}
	_, err = rt.Run(ctx, cri.RunSpec{
		Image:   spec.Image,
		Name:    spec.Name,
		Network: spec.Network,
		Labels: map[string]string{
			cri.LabelProject: spec.Project,
			cri.LabelRole:    Role,
			cri.LabelScope:   cri.ScopeLabel(spec.Project, spec.Scope),
			cri.LabelURN:     cri.URNLabel(spec.Project, spec.Scope, spec.Step),
			targetLabel:      spec.Target,
			poolLabel:        pool,
		},
		Cmd:   cmd,
		Ports: ports,
	})
	if err != nil {
		return Forwarder{}, fmt.Errorf("clusterrelay: start %s: %w", spec.Name, err)
	}
	info, err = rt.Inspect(ctx, spec.Name)
	if err != nil {
		return Forwarder{}, fmt.Errorf("clusterrelay: inspect %s: %w", spec.Name, err)
	}
	return forwarderFromInfo(info)
}

// LookupForwarder reports the running forwarder named name. The bool is
// false when it is absent or stopped.
func LookupForwarder(ctx context.Context, rt cri.Runtime, name string) (Forwarder, bool, error) {
	info, err := rt.Inspect(ctx, name)
	if err != nil {
		if errors.Is(err, cri.ErrNotFound) {
			return Forwarder{}, false, nil
		}
		return Forwarder{}, false, fmt.Errorf("clusterrelay: inspect %s: %w", name, err)
	}
	if !info.Running {
		return Forwarder{}, false, nil
	}
	fwd, err := forwarderFromInfo(info)
	if err != nil {
		return Forwarder{}, false, err
	}
	return fwd, true, nil
}

// forwarderFromInfo reads the published relay ports off an inspected
// forwarder container.
func forwarderFromInfo(info cri.Container) (Forwarder, error) {
	addr, ok := info.Ports[strconv.Itoa(NodePort)+"/tcp"]
	if !ok {
		return Forwarder{}, fmt.Errorf("clusterrelay: %s: %w", info.Name, ErrNoPublishedPort)
	}
	return Forwarder{Addr: addr, UDPAddrs: relay.UDPAddrsFromInfo(info)}, nil
}

// PodManifest is a Pod spec running kevin-relay in SOCKS5 mode, pinned to
// nodeName so it lands on the node the port mappings target. udpPoolSize is
// the number of UDP ASSOCIATE pool ports to bind, starting at
// UDPNodePortBase - zero adds neither a --udp-relay-ports arg nor any UDP
// ports entry, the same "no UDP capacity" default kevin-relay itself falls
// back to. imagePullPolicy is Never - the image is loaded locally, never
// pulled.
func PodManifest(nodeName, image string, udpPoolSize int) string {
	args := fmt.Sprintf(`"socks5-gateway", "--listen", ":%d"`, NodePort)
	var udpPorts strings.Builder
	if udpPoolSize > 0 {
		args += fmt.Sprintf(`, "--udp-relay-ports", "%d-%d"`, UDPNodePortBase, UDPNodePortBase+udpPoolSize-1)
		for i := range udpPoolSize {
			fmt.Fprintf(&udpPorts, "\n    - containerPort: %d\n      hostPort: %d\n      protocol: UDP",
				UDPNodePortBase+i, UDPNodePortBase+i)
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
`, nodeName, image, args, NodePort, NodePort, udpPorts.String())
}
