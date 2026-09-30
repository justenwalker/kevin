// Package clusterrelay builds the SOCKS5 relay pod manifest and reserves
// the host ports it needs, shared by every Kubernetes-cluster step type.
package clusterrelay

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/justenwalker/kevin/internal/relay"
)

// NodePort is the fixed port the SOCKS5 relay pod listens on for its TCP
// CONNECT gateway.
const NodePort = 1080

// UDPNodePortBase is the first node-internal port of the relay pod's UDP
// ASSOCIATE pool.
const UDPNodePortBase = 40000

// Ports is the set of host ports reserved for the SOCKS5 relay pod: one TCP
// port, plus a UDP ASSOCIATE pool sized by relay.UDPPoolSize().
type Ports struct {
	// TCP is the host port for the relay's SOCKS5 gateway. Zero means no
	// relay is wanted at all.
	TCP int

	// UDP is the host ports reserved for the relay's UDP ASSOCIATE pool,
	// one per pool port, in order starting at UDPNodePortBase. Empty when
	// the pool is disabled (KEVIN_RELAY_UDP_POOL_SIZE=0).
	UDP []int
}

// Addr is the loopback address the relay's SOCKS5 port is published on, for
// a given host port.
func Addr(hostPort int) string {
	return fmt.Sprintf("127.0.0.1:%d", hostPort)
}

// UDPAddrs maps each node-internal UDP pool port to its host address, the
// shape plugin.ExposedPort.RelayUDPAddrs expects.
func UDPAddrs(hostPorts []int) map[string]string {
	if len(hostPorts) == 0 {
		return nil
	}
	addrs := make(map[string]string, len(hostPorts))
	for i, hostPort := range hostPorts {
		addrs[strconv.Itoa(UDPNodePortBase+i)] = Addr(hostPort)
	}
	return addrs
}

// PickPorts asks the OS for a fresh TCP relay port and, when
// relay.UDPPoolSize() is nonzero, a fresh UDP ASSOCIATE pool of that size.
func PickPorts(ctx context.Context) (Ports, error) {
	tcp, err := findFreePort(ctx)
	if err != nil {
		return Ports{}, fmt.Errorf("clusterrelay: pick a port for the relay: %w", err)
	}
	poolSize, err := relay.UDPPoolSize()
	if err != nil {
		return Ports{}, fmt.Errorf("clusterrelay: %w", err)
	}
	if poolSize == 0 {
		return Ports{TCP: tcp}, nil
	}
	udp, err := findFreePorts(ctx, poolSize)
	if err != nil {
		return Ports{}, fmt.Errorf("clusterrelay: pick ports for the relay's udp pool: %w", err)
	}
	return Ports{TCP: tcp, UDP: udp}, nil
}

// findFreePort asks the OS for a free port on the host.
// There's an unavoidable race between closing this listener and the
// cluster tool binding the same port for real.
func findFreePort(ctx context.Context) (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("clusterrelay: pick a free port: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("clusterrelay: unexpected listener address type %T", ln.Addr())
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
			return nil, fmt.Errorf("clusterrelay: pick a free port: %w", err)
		}
		lns = append(lns, ln)
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("clusterrelay: unexpected listener address type %T", ln.Addr())
		}
		ports[i] = addr.Port
	}
	return ports, nil
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
