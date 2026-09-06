package main

import (
	"fmt"
	"net/netip"
	"sync"
)

// fakeIPPool allocates a stable synthetic address per hostname out of a
// configured IPv4 and IPv6 prefix, and looks an address back up to the
// hostname it was allocated for - what a captured connection's original
// destination (see origDst) is matched against, with no need to parse the
// connection's payload at all. Allocation is for the life of the process:
// there is no eviction, and the default prefixes (a /15 and a /64) are
// large enough that a dev session never comes close to exhausting one.
type fakeIPPool struct {
	v4, v6 netip.Prefix

	mu     sync.Mutex
	byHost map[string]selfAddrs
	byAddr map[netip.Addr]string
	nextV4 uint64
	nextV6 uint64
}

// newFakeIPPool parses v4Range and v6Range as CIDRs. Both must be valid -
// callers should surface either error at startup, not treat a bad range as
// meaning "no fake-IP support".
func newFakeIPPool(v4Range, v6Range string) (*fakeIPPool, error) {
	v4, err := netip.ParsePrefix(v4Range)
	if err != nil {
		return nil, fmt.Errorf("relay: parse fake ipv4 range: %w", err)
	}
	v6, err := netip.ParsePrefix(v6Range)
	if err != nil {
		return nil, fmt.Errorf("relay: parse fake ipv6 range: %w", err)
	}
	return &fakeIPPool{
		v4:     v4.Masked(),
		v6:     v6.Masked(),
		byHost: make(map[string]selfAddrs),
		byAddr: make(map[netip.Addr]string),
	}, nil
}

// allocate returns the synthetic address host is already assigned, or
// assigns and returns a fresh one - one address from each of the pool's
// families, so a query for either A or AAAA resolves the same host to a
// stable address either way.
func (p *fakeIPPool) allocate(host string) (selfAddrs, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if addrs, ok := p.byHost[host]; ok {
		return addrs, nil
	}

	v4, err := p.next(p.v4, &p.nextV4)
	if err != nil {
		return selfAddrs{}, fmt.Errorf("relay: allocate fake ipv4 for %q: %w", host, err)
	}
	v6, err := p.next(p.v6, &p.nextV6)
	if err != nil {
		return selfAddrs{}, fmt.Errorf("relay: allocate fake ipv6 for %q: %w", host, err)
	}

	addrs := selfAddrs{V4: v4.String(), V6: v6.String()}
	p.byHost[host] = addrs
	p.byAddr[v4] = host
	p.byAddr[v6] = host
	return addrs, nil
}

// next returns the next unused address in prefix, starting just past the
// network address, and advances counter. It errors once counter would
// overflow the prefix's own address space.
func (p *fakeIPPool) next(prefix netip.Prefix, counter *uint64) (netip.Addr, error) {
	*counter++
	addr := addOffset(prefix.Addr(), *counter)
	if !prefix.Contains(addr) {
		return netip.Addr{}, fmt.Errorf("relay: %s is exhausted", prefix)
	}
	return addr, nil
}

// lookup reports the host addr was allocated for, if any.
func (p *fakeIPPool) lookup(addr netip.Addr) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	host, ok := p.byAddr[addr]
	return host, ok
}

// addOffset returns base plus offset, treated as a big-endian integer added
// to base's address bytes - base.As16() for both families, so the same
// carry-propagating loop handles a 4-byte or a 16-byte address alike; the
// low bytes are the same either way; only the value of Is4() differs.
func addOffset(base netip.Addr, offset uint64) netip.Addr {
	b := base.As16()
	carry := offset
	for i := 15; i >= 0 && carry > 0; i-- {
		sum := uint64(b[i]) + carry&0xff
		b[i] = byte(sum) //nolint:gosec // truncation to the low byte is the point: sum's high bits are next iteration's carry
		carry = carry>>8 + sum>>8
	}
	addr := netip.AddrFrom16(b)
	if base.Is4() {
		addr = addr.Unmap()
	}
	return addr
}
