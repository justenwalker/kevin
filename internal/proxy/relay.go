package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	socks5 "golang.org/x/net/proxy"
)

// splitSOCKS5 splits a "socks5://<relay>/<target>" address into its relay
// and target parts. ok is false when address carries no socks5:// prefix.
// Mirrors internal/engine/socks5forward.go's identical helper - not
// importable here, proxy sits below supervisor in the dependency graph.
func splitSOCKS5(address string) (string, string, bool) {
	rest, ok := strings.CutPrefix(address, "socks5://")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/")
}

// socks5TargetKey is the request-context key that carries the real
// upstream address a route's Upstream (itself a socks5:// relay address)
// must be CONNECTed to, once forward has already rewritten r.URL.Host to
// the relay's own dialable address.
type socks5TargetKey struct{}

// withSOCKS5Target attaches target to ctx, for dialContext to read.
func withSOCKS5Target(ctx context.Context, target string) context.Context {
	return context.WithValue(ctx, socks5TargetKey{}, target)
}

// socks5TargetFromContext returns the target that withSOCKS5Target attached,
// if any.
func socks5TargetFromContext(ctx context.Context) (string, bool) {
	target, ok := ctx.Value(socks5TargetKey{}).(string)
	return target, ok
}

// dialContext is p.rp's Transport.DialContext, and also what serveUpgrade
// dials through. For an ordinary route it is a plain TCP dial. For a route
// whose Upstream is a socks5:// relay address, forward has already
// rewritten addr to the relay's own dialable address and attached the real
// target to ctx - dialContext then dials addr and issues a SOCKS5 CONNECT
// for that target, instead of treating addr itself as the destination.
func (p *Proxy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	target, ok := socks5TargetFromContext(ctx)
	if !ok {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("proxy: dial %s: %w", addr, err)
		}
		return conn, nil
	}
	return dialViaSOCKS5(ctx, network, addr, target)
}

// dialViaSOCKS5 dials relayAddr and asks it to CONNECT to target. Mirrors
// internal/engine/socks5forward.go's newPortForward dial exactly.
func dialViaSOCKS5(ctx context.Context, network, relayAddr, target string) (net.Conn, error) {
	dialer, err := socks5.SOCKS5(network, relayAddr, nil, socks5.Direct)
	if err != nil {
		return nil, fmt.Errorf("proxy: socks5 dialer for %s: %w", relayAddr, err)
	}
	contextDialer, ok := dialer.(socks5.ContextDialer)
	if !ok {
		return nil, errors.New("proxy: socks5 dialer does not support DialContext")
	}
	conn, err := contextDialer.DialContext(ctx, network, target)
	if err != nil {
		return nil, fmt.Errorf("proxy: socks5 connect via %s to %s: %w", relayAddr, target, err)
	}
	return conn, nil
}
