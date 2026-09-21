package relay

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/justenwalker/kevin/internal/cri"
)

// UDPPoolSizeEnvVar overrides the number of UDP relay ports the relay
// container publishes for SOCKS5 UDP ASSOCIATE sessions - a host/local-dev
// setting, not a kevin.cue field, read directly here and by
// internal/plugins/kind so both sides agree on the pool size without
// either learning it from the other.
const UDPPoolSizeEnvVar = "KEVIN_RELAY_UDP_POOL_SIZE"

// defaultUDPPoolSize is the number of UDP relay ports published when
// UDPPoolSizeEnvVar is unset.
const defaultUDPPoolSize = 16

// udpRelayPortBase is the first container-side port in the relay's fixed
// UDP ASSOCIATE pool. The container publishes udpPoolSize() consecutive
// ports starting here, so a SOCKS5 ASSOCIATE session can bind one before
// whatever needs it (a container, a kind pod) exists.
const udpRelayPortBase = 40000

// UDPPoolSize reads UDPPoolSizeEnvVar, falling back to defaultUDPPoolSize
// when unset - shared by this package and internal/plugins/kind so both
// sides of the pool (the relay container's published ports and, for kind,
// the cluster's own extraPortMappings/Pod hostPorts) agree on its size
// without either learning it from the other.
func UDPPoolSize() (int, error) {
	v := os.Getenv(UDPPoolSizeEnvVar)
	if v == "" {
		return defaultUDPPoolSize, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, ErrInvalidUDPPoolSize
	}
	return n, nil
}

// udpRelayPorts lists n container-side UDP ports, starting at
// udpRelayPortBase.
func udpRelayPorts(n int) []int {
	ports := make([]int, n)
	for i := range ports {
		ports[i] = udpRelayPortBase + i
	}
	return ports
}

// udpRelayPortsArg appends --udp-relay-ports naming the n-port range
// starting at udpRelayPortBase to args, or leaves args untouched when n is
// zero - kevin-relay's own flag then defaults to no UDP ASSOCIATE capacity.
func udpRelayPortsArg(n int, args []string) []string {
	if n == 0 {
		return args
	}
	return append(args, "--udp-relay-ports", fmt.Sprintf("%d-%d", udpRelayPortBase, udpRelayPortBase+n-1))
}

// udpAddrsFromInfo builds a container-port -> host-address map from every
// UDP port info publishes - whatever the relay's UDP pool actually
// published when it was created, regardless of the caller's own current
// UDPPoolSizeEnvVar (a container reused across a pool-size change keeps
// its original pool; see docs/site/content/docs/concepts/relay.md).
func udpAddrsFromInfo(info cri.Container) map[string]string {
	var addrs map[string]string
	for port, addr := range info.Ports {
		containerPort, ok := strings.CutSuffix(port, "/udp")
		if !ok {
			continue
		}
		if addrs == nil {
			addrs = make(map[string]string, len(info.Ports))
		}
		addrs[containerPort] = addr
	}
	return addrs
}
