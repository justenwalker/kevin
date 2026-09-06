package main

import (
	"context"
	"net"
)

// lookupOrigDst is origDst, indirected through a var so a test can swap in a
// stub - the match branch below needs a real DNAT'd connection to exercise
// origDst itself, but dispatch's own routing logic (matched address ->
// tunnel, anything else -> fallback) doesn't, and shouldn't need one.
var lookupOrigDst = origDst

// dispatch checks conn's original destination (before nftables DNAT
// rewrote it) against pool - if it matches a registered fake-IP, the real
// host is already known, so the connection tunnels directly with no
// protocol parsing at all; otherwise fallback runs, today's SNI/Host-based
// dispatch for the general, unregistered capture case.
func dispatch(ctx context.Context, conn net.Conn, proxyAddr string, port int, pool *fakeIPPool, fallback func(net.Conn)) {
	if dst, err := lookupOrigDst(conn); err == nil {
		if host, ok := pool.lookup(dst.Addr()); ok {
			tunnel(ctx, conn, conn, proxyAddr, host, port, nil)
			return
		}
	}
	fallback(conn)
}
