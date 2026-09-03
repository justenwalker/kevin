package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
)

// tlsServerNameKey is the request-context key that carries the hostname a
// tls: true route's certificate must be verified against - the name the
// client actually asked for, not the dial address, which for a relay route
// is the relay's own address (see relay.go's socks5TargetKey).
type tlsServerNameKey struct{}

// withTLSServerName attaches serverName to ctx, for dialTLSContext to read.
func withTLSServerName(ctx context.Context, serverName string) context.Context {
	return context.WithValue(ctx, tlsServerNameKey{}, serverName)
}

// tlsServerNameFromContext returns the server name that withTLSServerName
// attached, if any.
func tlsServerNameFromContext(ctx context.Context) (string, bool) {
	serverName, ok := ctx.Value(tlsServerNameKey{}).(string)
	return serverName, ok
}

// dialTLSContext is p.rp's Transport.DialTLSContext for a tls: true route.
// It dials via p.dialContext - so a relay-routed upstream still gets a
// SOCKS5 CONNECT to the real target - then completes the TLS handshake
// itself, with ServerName taken from the request context rather than addr:
// addr is only ever a dial address, and for a relay route that address is
// the relay's own, not the upstream's real identity.
func (p *Proxy) dialTLSContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := p.dialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}

	serverName, ok := tlsServerNameFromContext(ctx)
	if !ok || serverName == "" {
		if serverName, _, err = net.SplitHostPort(addr); err != nil {
			serverName = addr
		}
	}

	tlsConn := tls.Client(conn, &tls.Config{
		ServerName: serverName,
		RootCAs:    p.rootPool,
		MinVersion: tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("proxy: tls handshake with %s (server name %q): %w", addr, serverName, err)
	}
	return tlsConn, nil
}
