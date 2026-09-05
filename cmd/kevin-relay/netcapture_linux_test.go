//go:build linux

package main

import (
	"bytes"
	"net"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// newRuleCountingConn builds an nftables.Conn over a fake netlink dial that
// acks every request (mirroring nftables' own test pattern) and counts how
// many NEWRULE messages it sees, keyed by which IP family the rule's NAT
// expression targets.
func newRuleCountingConn(t *testing.T) (*nftables.Conn, func() map[byte]int) {
	t.Helper()

	counts := make(map[byte]int)
	dial := func(req []netlink.Message) ([]netlink.Message, error) {
		newRuleType := netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | unix.NFT_MSG_NEWRULE)
		for _, msg := range req {
			if msg.Header.Type != newRuleType {
				continue
			}
			counts[msg.Data[0]]++ // the nfgenmsg family byte leads every nftables message payload
		}
		return req, nil
	}

	conn, err := nftables.New(nftables.WithTestDial(dial))
	require.NoError(t, err)
	return conn, func() map[byte]int { return counts }
}

// totalRules sums every family's rule count from newRuleCountingConn.
func totalRules(counts map[byte]int) int {
	total := 0
	for _, c := range counts {
		total += c
	}
	return total
}

func TestBuildCaptureRuleset(t *testing.T) {
	t.Run("one rule per port for an ipv4-only relay", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{80, 443}, selfAddrs{V4: "10.20.30.40"}, nil)
		require.NoError(t, err)
		assert.Equal(t, map[byte]int{unix.NFPROTO_INET: 2}, counts(),
			"one rule per port, tagged with the table's own inet family regardless of which L3 family the rule matches on")
	})

	t.Run("two rules per port for a dual-stack relay", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{443}, selfAddrs{V4: "10.20.30.40", V6: "fd00::5"}, nil)
		require.NoError(t, err)
		assert.Equal(t, map[byte]int{unix.NFPROTO_INET: 2}, counts(),
			"a v4 rule and a v6 rule for the one port")
	})

	t.Run("no rules when the relay has no address at all", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{80, 443}, selfAddrs{}, nil)
		require.NoError(t, err)
		assert.Empty(t, counts())
	})

	t.Run("a router registration adds one exclude rule per cidr", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{443}, selfAddrs{V4: "10.20.30.40"},
			[]string{"10.244.0.0/16", "10.96.0.0/12"})
		require.NoError(t, err)
		assert.Equal(t, 3, totalRules(counts()), "2 exclude rules plus 1 per-port v4 rule")
	})

	t.Run("an invalid cidr fails the whole apply", func(t *testing.T) {
		conn, _ := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{443}, selfAddrs{V4: "10.20.30.40"}, []string{"not-a-cidr"})
		require.Error(t, err)
	})

	t.Run("exclude rules come before per-port dnat rules", func(t *testing.T) {
		conn, kinds := newRuleKindRecordingConn(t)

		err := buildCaptureRuleset(conn, []int{443}, selfAddrs{V4: "10.20.30.40"}, []string{"10.244.0.0/16"})
		require.NoError(t, err)
		assert.Equal(t, []string{"exclude", "dnat"}, kinds())
	})
}

// newRuleKindRecordingConn builds an nftables.Conn like newRuleCountingConn,
// but records each NEWRULE as "dnat" or "exclude" in queue order, so a test
// can assert exclude rules precede DNAT rules in the built chain. It tells
// them apart by whether the message's marshaled expressions include a "nat"
// expr (captureRule's trailing NAT) - excludeRule's own expressions (meta,
// payload, bitwise, cmp, verdict) never produce that name.
func newRuleKindRecordingConn(t *testing.T) (*nftables.Conn, func() []string) {
	t.Helper()

	var kinds []string
	dial := func(req []netlink.Message) ([]netlink.Message, error) {
		newRuleType := netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES << 8) | unix.NFT_MSG_NEWRULE)
		for _, msg := range req {
			if msg.Header.Type != newRuleType {
				continue
			}
			if bytes.Contains(msg.Data, []byte("nat\x00")) {
				kinds = append(kinds, "dnat")
			} else {
				kinds = append(kinds, "exclude")
			}
		}
		return req, nil
	}

	conn, err := nftables.New(nftables.WithTestDial(dial))
	require.NoError(t, err)
	return conn, func() []string { return kinds }
}

func TestCaptureHook(t *testing.T) {
	t.Run("output for a workload, no exclusions", func(t *testing.T) {
		hook, name := captureHook(nil)
		assert.Same(t, nftables.ChainHookOutput, hook)
		assert.Equal(t, captureOutputChainName, name)
	})

	t.Run("prerouting for a router, exclusions set", func(t *testing.T) {
		hook, name := captureHook([]string{"10.244.0.0/16"})
		assert.Same(t, nftables.ChainHookPrerouting, hook)
		assert.Equal(t, capturePreroutingChainName, name)
	})
}

func TestCaptureRule(t *testing.T) {
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: captureTableName}
	chain := &nftables.Chain{Name: captureOutputChainName, Table: table}

	rule := captureRule(table, chain, unix.NFPROTO_IPV4, []byte{10, 20, 30, 40}, 443)

	require.Same(t, table, rule.Table)
	require.Same(t, chain, rule.Chain)

	nat, ok := rule.Exprs[len(rule.Exprs)-1].(*expr.NAT)
	require.True(t, ok, "the last expression must be the nat itself")
	assert.Equal(t, expr.NATTypeDestNAT, nat.Type)
	assert.Equal(t, uint32(unix.NFPROTO_IPV4), nat.Family)
	assert.True(t, nat.Specified, "an unspecified nat leaves the port untranslated")

	addr, ok := rule.Exprs[len(rule.Exprs)-3].(*expr.Immediate)
	require.True(t, ok, "the dnat address must be loaded into a register just before the nat itself")
	assert.Equal(t, []byte{10, 20, 30, 40}, addr.Data)
}

func TestExcludeRule(t *testing.T) {
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: captureTableName}
	chain := &nftables.Chain{Name: capturePreroutingChainName, Table: table}

	t.Run("an ipv4 cidr", func(t *testing.T) {
		rule, err := excludeRule(table, chain, "10.244.0.0/16")
		require.NoError(t, err)
		require.Same(t, table, rule.Table)
		require.Same(t, chain, rule.Chain)

		verdict, ok := rule.Exprs[len(rule.Exprs)-1].(*expr.Verdict)
		require.True(t, ok, "the last expression must be the verdict itself")
		assert.Equal(t, expr.VerdictReturn, verdict.Kind)

		cmp, ok := rule.Exprs[len(rule.Exprs)-2].(*expr.Cmp)
		require.True(t, ok, "the network address compare must come just before the verdict")
		assert.True(t, net.IP(cmp.Data).Equal(net.ParseIP("10.244.0.0")))

		payload, ok := rule.Exprs[2].(*expr.Payload)
		require.True(t, ok)
		assert.Equal(t, uint32(16), payload.Offset, "ipv4 daddr sits at network header offset 16")
		assert.Equal(t, uint32(4), payload.Len)
	})

	t.Run("an ipv6 cidr", func(t *testing.T) {
		rule, err := excludeRule(table, chain, "fd00::/8")
		require.NoError(t, err)

		payload, ok := rule.Exprs[2].(*expr.Payload)
		require.True(t, ok)
		assert.Equal(t, uint32(24), payload.Offset, "ipv6 daddr sits at network header offset 24")
		assert.Equal(t, uint32(16), payload.Len)
	})

	t.Run("an invalid cidr", func(t *testing.T) {
		_, err := excludeRule(table, chain, "not-a-cidr")
		require.Error(t, err)
	})
}
