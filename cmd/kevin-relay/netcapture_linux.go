//go:build linux

package main

import (
	"fmt"
	"net"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// captureTableName names the nftables table applyCapture installs inside a
// target network namespace. captureOutputChainName and
// capturePreroutingChainName name its two possible chains - a workload
// namespace (no excludeCIDRs) gets the output chain, capturing its own
// outbound connections; a router namespace (excludeCIDRs set, a
// builtin:kind node) gets the prerouting chain instead, capturing what
// transits it.
const (
	captureTableName           = "kevin"
	captureOutputChainName     = "output"
	capturePreroutingChainName = "prerouting"
)

// captureHook picks the chain hook and name a capture registration gets:
// the output hook for a workload's own namespace (no excludeCIDRs), the
// prerouting hook for a router namespace (excludeCIDRs set).
func captureHook(excludeCIDRs []string) (*nftables.ChainHook, string) {
	if len(excludeCIDRs) > 0 {
		return nftables.ChainHookPrerouting, capturePreroutingChainName
	}
	return nftables.ChainHookOutput, captureOutputChainName
}

// applyCapture installs the transparent-capture nftables ruleset inside the
// network namespace at netnsPath. With no excludeCIDRs, it's an
// output-hook nat chain that DNATs the namespace's own outbound TCP on
// each of ports to self, on whichever address family the connection uses.
// With excludeCIDRs, it's a prerouting-hook chain instead: the same
// per-port DNAT, but only for a destination outside excludeCIDRs - a
// cluster's own pod and service subnets, so pod-to-pod and pod-to-service
// traffic passing through this namespace as a router isn't touched. Safe
// to call more than once for the same namespace - the chain's rules are
// replaced, not accumulated, so a later call with a different port set or
// exclusion list fully supersedes an earlier one.
func applyCapture(netnsPath string, ports []int, self selfAddrs, excludeCIDRs []string) error {
	ns, err := os.Open(netnsPath) //nolint:gosec // netnsPath comes from the engine over the control channel, not user input
	if err != nil {
		return fmt.Errorf("relay: open netns %s: %w", netnsPath, err)
	}
	defer func() { _ = ns.Close() }()

	conn, err := nftables.New(nftables.WithNetNSFd(int(ns.Fd())))
	if err != nil {
		return fmt.Errorf("relay: connect netlink in %s: %w", netnsPath, err)
	}

	if err := buildCaptureRuleset(conn, ports, self, excludeCIDRs); err != nil {
		return fmt.Errorf("relay: apply capture in %s: %w", netnsPath, err)
	}
	return nil
}

// buildCaptureRuleset queues and flushes the capture table/chain/rules on
// conn - split out from applyCapture so a test can supply a
// [nftables.WithTestDial] connection instead of a real netns, the same way
// the nftables package's own tests verify rule shape without a kernel.
func buildCaptureRuleset(conn *nftables.Conn, ports []int, self selfAddrs, excludeCIDRs []string) error {
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: captureTableName})

	hook, name := captureHook(excludeCIDRs)
	chain := conn.AddChain(&nftables.Chain{
		Name:     name,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  hook,
		Priority: nftables.ChainPriorityNATDest,
	})
	conn.FlushChain(chain)

	for _, cidr := range excludeCIDRs {
		rule, err := excludeRule(table, chain, cidr)
		if err != nil {
			return fmt.Errorf("exclude %q: %w", cidr, err)
		}
		conn.AddRule(rule)
	}

	for _, port := range ports {
		if self.V4 != "" {
			conn.AddRule(captureRule(table, chain, unix.NFPROTO_IPV4, net.ParseIP(self.V4).To4(), port))
		}
		if self.V6 != "" {
			conn.AddRule(captureRule(table, chain, unix.NFPROTO_IPV6, net.ParseIP(self.V6).To16(), port))
		}
	}

	return conn.Flush() //nolint:wrapcheck // applyCapture wraps this with the netns path; a test calls this directly and wants the raw error
}

// captureRule builds a rule that DNATs outbound TCP on port, for a
// connection of family (unix.NFPROTO_IPV4 or unix.NFPROTO_IPV6), to
// addr:port. nftables rules are an untyped, register-based instruction
// list - each match loads a value into register 1 and compares it, and the
// final NAT reads its target address/port back out of registers 1 and 2,
// so the pairing between an expr and the one before or after it isn't
// visible from the types alone.
func captureRule(table *nftables.Table, chain *nftables.Chain, family byte, addr net.IP, port int) *nftables.Rule {
	return &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}},

			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},

			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(port))}, //nolint:gosec // port is validated elsewhere to fit uint16

			&expr.Immediate{Register: 1, Data: addr},
			&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(uint16(port))}, //nolint:gosec // port is validated elsewhere to fit uint16
			&expr.NAT{
				Type:        expr.NATTypeDestNAT,
				Family:      uint32(family),
				RegAddrMin:  1,
				RegProtoMin: 2,
				Specified:   true,
			},
		},
	}
}

// networkHeaderDstOffset is the byte offset and length of the destination
// address field within an IPv4 or IPv6 header, as nft's own compiler emits
// for "ip daddr"/"ip6 daddr": 4 bytes at offset 16 for IPv4, 16 bytes at
// offset 24 for IPv6.
func networkHeaderDstOffset(v4 bool) (offset, length uint32) { //nolint:nonamedreturns // two same-typed returns; the names document which is which
	if v4 {
		return 16, 4
	}
	return 24, 16
}

// excludeRule builds a rule that returns from chain - skipping every rule
// after it, including captureRule's DNAT rules - for a packet whose
// destination falls inside cidr. cidr may be IPv4 or IPv6; the rule matches
// only that family, since network-header field width and offset both
// differ by family.
func excludeRule(table *nftables.Table, chain *nftables.Chain, cidr string) (*nftables.Rule, error) {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("relay: parse cidr: %w", err)
	}

	v4 := network.IP.To4() != nil
	family := byte(unix.NFPROTO_IPV6)
	addr := network.IP.To16()
	if v4 {
		family = unix.NFPROTO_IPV4
		addr = network.IP.To4()
	}
	mask := []byte(network.Mask) // net.ParseCIDR already sizes this to match addr: 4 bytes for a v4 CIDR, 16 for v6
	offset, length := networkHeaderDstOffset(v4)

	return &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}},

			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: length},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: length, Mask: mask, Xor: make([]byte, length)},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: addr},

			&expr.Verdict{Kind: expr.VerdictReturn},
		},
	}, nil
}
