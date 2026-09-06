//go:build !linux

package main

import (
	"net"
	"net/netip"
)

// origDst always fails on a non-linux build - the relay only ever ships as
// a linux image, so this only matters for a build run directly on a
// contributor's own machine. Callers treat that the same as "no fake-IP
// match," falling through to SNI/Host dispatch.
func origDst(net.Conn) (netip.AddrPort, error) {
	return netip.AddrPort{}, ErrOrigDstUnsupported
}
