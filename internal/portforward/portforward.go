// Package portforward opens local loopback listeners that carry traffic
// through a relay's SOCKS5 listener to a target inside the project network.
package portforward

import (
	"fmt"
	"strings"

	"github.com/justenwalker/kevin/internal/logging"
)

var log = logging.New("portforward")

// Spec describes one forward.
type Spec struct {
	// Name labels the forward in errors.
	Name string

	// Relay is the host-reachable address of the relay's SOCKS5 listener, and
	// Target the host:port to reach through it.
	Relay, Target string

	// HostPort is the loopback port to listen on, or 0 for any free port.
	HostPort int

	// RelayUDPAddrs maps the relay's UDP pool port to
	// a host-reachable address. Only a UDP forward reads it.
	RelayUDPAddrs map[int]string
}

// SplitSOCKS5 splits a "socks5://<relay>/<target>" address into its relay
// and target parts. ok is false when address carries no socks5:// prefix or
// no target after the relay.
func SplitSOCKS5(address string) (string, string, bool) {
	rest, ok := strings.CutPrefix(address, "socks5://")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/")
}

// listenAddr is the loopback address a forward for spec listens on.
func (s Spec) listenAddr() string {
	if s.HostPort > 0 {
		return fmt.Sprintf("127.0.0.1:%d", s.HostPort)
	}
	return "127.0.0.1:0"
}
