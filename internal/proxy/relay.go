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

// socks5RelayKey is the request-context key that carries the relay a route's
// Upstream (itself a socks5:// relay address) must be CONNECTed through.
type socks5RelayKey struct{}

// withSOCKS5Relay attaches relay to ctx, for dialContext to read.
func withSOCKS5Relay(ctx context.Context, relay string) context.Context {
	return context.WithValue(ctx, socks5RelayKey{}, relay)
}

// socks5RelayFromContext returns the relay that withSOCKS5Relay attached, if
// any.
func socks5RelayFromContext(ctx context.Context) (string, bool) {
	relay, ok := ctx.Value(socks5RelayKey{}).(string)
	return relay, ok
}

// dialContext is p.rp's Transport.DialContext, and also what serveUpgrade
// dials through. For an ordinary route it is a plain TCP dial to addr. For a
// route whose Upstream is a socks5:// relay address, addr is already the
// real target - forward left r.URL.Host alone - and ctx carries the relay
// to CONNECT through.
func (p *Proxy) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	relay, ok := socks5RelayFromContext(ctx)
	if !ok {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("proxy: dial %s: %w", addr, err)
		}
		return conn, nil
	}
	return dialViaSOCKS5(ctx, network, relay, addr)
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
