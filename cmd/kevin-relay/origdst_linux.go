//go:build linux

package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ip6tSoOriginalDst is IP6T_SO_ORIGINAL_DST from
// linux/netfilter_ipv6/ip6_tables.h - golang.org/x/sys/unix only carries
// the IPv4 SO_ORIGINAL_DST (also numerically 80, but at a different
// getsockopt level).
const ip6tSoOriginalDst = 80

// origDst reads the pre-NAT destination of conn, a connection accepted on
// a listener a DNAT rule redirected - the kernel's conntrack table keeps
// the original destination available via this getsockopt even after the
// redirect. This is how a fake-IP-matched connection is told apart from
// an ordinary one, with no need to touch its payload at all.
//
// kevin-relay's listeners all bind a wildcard address ("<":443>", etc.),
// which Go/Linux makes a dual-stack AF_INET6 socket - every accepted
// connection's underlying fd is IPv6, even one from an IPv4 peer or to an
// IPv4-mapped destination, so the IPv6 getsockopt is tried first; a
// listener that somehow isn't dual-stack (a IPv4-only "tcp4" one, which
// kevin-relay doesn't use today) falls back to the IPv4 form.
func origDst(conn net.Conn) (netip.AddrPort, error) {
	tc, ok := conn.(*net.TCPConn)
	if !ok {
		return netip.AddrPort{}, errors.New("relay: origdst: not a TCP connection")
	}
	sc, err := tc.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("relay: origdst: syscall conn: %w", err)
	}

	var addr netip.AddrPort
	var opErr error
	ctrlErr := sc.Control(func(fd uintptr) {
		addr, opErr = getOrigDst6(int(fd))
		if opErr != nil {
			addr, opErr = getOrigDst4(int(fd))
		}
	})
	if ctrlErr != nil {
		return netip.AddrPort{}, fmt.Errorf("relay: origdst: control: %w", ctrlErr)
	}
	if opErr != nil {
		return netip.AddrPort{}, opErr
	}
	return addr, nil
}

// getOrigDst4 reads SO_ORIGINAL_DST for an AF_INET socket.
func getOrigDst4(fd int) (netip.AddrPort, error) {
	var raw unix.RawSockaddrInet4
	if err := getsockopt(fd, unix.SOL_IP, unix.SO_ORIGINAL_DST, unsafe.Pointer(&raw), uint32(unsafe.Sizeof(raw))); err != nil {
		return netip.AddrPort{}, fmt.Errorf("relay: origdst: getsockopt ipv4: %w", err)
	}
	return netip.AddrPortFrom(netip.AddrFrom4(raw.Addr), swapPort(raw.Port)), nil
}

// getOrigDst6 reads IP6T_SO_ORIGINAL_DST for an AF_INET6 socket. The
// returned address is unmapped when it's an IPv4-mapped IPv6 address (the
// original destination was actually IPv4), so a caller always gets the
// plain family the fakeIPPool allocated it from.
func getOrigDst6(fd int) (netip.AddrPort, error) {
	var raw unix.RawSockaddrInet6
	if err := getsockopt(fd, unix.IPPROTO_IPV6, ip6tSoOriginalDst, unsafe.Pointer(&raw), uint32(unsafe.Sizeof(raw))); err != nil {
		return netip.AddrPort{}, fmt.Errorf("relay: origdst: getsockopt ipv6: %w", err)
	}
	return netip.AddrPortFrom(netip.AddrFrom16(raw.Addr).Unmap(), swapPort(raw.Port)), nil
}

// swapPort corrects sockaddr_in{,6}.sin_port, always network (big-endian)
// byte order on the wire, back to a host-native value - RawSockaddrInet{4,6}
// declares Port as a plain uint16, so reading it already reinterpreted the
// wire bytes in the host's own order.
func swapPort(port uint16) uint16 {
	return port<<8 | port>>8
}

// getsockopt reads level/opt into buf, sized bufLen, via the raw syscall -
// golang.org/x/sys/unix has no typed wrapper for either SO_ORIGINAL_DST
// form.
func getsockopt(fd, level, opt int, buf unsafe.Pointer, bufLen uint32) error {
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, uintptr(fd), uintptr(level), uintptr(opt),
		uintptr(buf), uintptr(unsafe.Pointer(&bufLen)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
