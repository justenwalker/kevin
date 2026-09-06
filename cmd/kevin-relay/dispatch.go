package main

import (
	"context"
	"net"
)

// dispatch checks conn's original destination (before nftables DNAT
// rewrote it) against pool - if it matches a registered fake-IP, the real
// host is already known, so the connection tunnels directly with no
// protocol parsing at all; otherwise fallback runs, today's SNI/Host-based
// dispatch for the general, unregistered capture case.
func dispatch(ctx context.Context, conn net.Conn, proxyAddr string, port int, pool *fakeIPPool, fallback func(net.Conn)) {
	if dst, err := origDst(conn); err == nil {
		if host, ok := pool.lookup(dst.Addr()); ok {
			tunnel(ctx, conn, conn, proxyAddr, host, port, nil)
			return
		}
	}
	fallback(conn)
}
