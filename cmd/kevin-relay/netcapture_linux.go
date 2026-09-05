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

// captureTableName and captureChainName name the nftables table and chain
// applyCapture installs inside a target container's network namespace.
const (
	captureTableName = "kevin"
	captureChainName = "output"
)

// applyCapture installs the transparent-capture nftables ruleset inside the
// network namespace at netnsPath: an inet-family table with an output-hook
// nat chain that DNATs outbound TCP on each of ports to self, on whichever
// address family the connection uses. Safe to call more than once for the
// same namespace - the chain's rules are replaced, not accumulated, so a
// later call with a different port set fully supersedes an earlier one.
func applyCapture(netnsPath string, ports []int, self selfAddrs) error {
	ns, err := os.Open(netnsPath) //nolint:gosec // netnsPath comes from the engine over the control channel, not user input
	if err != nil {
		return fmt.Errorf("relay: open netns %s: %w", netnsPath, err)
	}
	defer func() { _ = ns.Close() }()

	conn, err := nftables.New(nftables.WithNetNSFd(int(ns.Fd())))
	if err != nil {
		return fmt.Errorf("relay: connect netlink in %s: %w", netnsPath, err)
	}

	if err := buildCaptureRuleset(conn, ports, self); err != nil {
		return fmt.Errorf("relay: apply capture in %s: %w", netnsPath, err)
	}
	return nil
}

// buildCaptureRuleset queues and flushes the capture table/chain/rules on
// conn - split out from applyCapture so a test can supply a
// [nftables.WithTestDial] connection instead of a real netns, the same way
// the nftables package's own tests verify rule shape without a kernel.
func buildCaptureRuleset(conn *nftables.Conn, ports []int, self selfAddrs) error {
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyINet, Name: captureTableName})
	chain := conn.AddChain(&nftables.Chain{
		Name:     captureChainName,
		Table:    table,
		Type:     nftables.ChainTypeNAT,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityNATDest,
	})
	conn.FlushChain(chain)

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
// addr:port.
func captureRule(table *nftables.Table, chain *nftables.Chain, family byte, addr net.IP, port int) *nftables.Rule {
	return &nftables.Rule{
		Table: table,
		Chain: chain,
		Exprs: []expr.Any{
			// this rule only applies to the address family addr is in
			&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}},

			// tcp only
			&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},

			// matching the declared destination port
			&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: binaryutil.BigEndian.PutUint16(uint16(port))}, //nolint:gosec // port is validated elsewhere to fit uint16

			// dnat to addr:port
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
