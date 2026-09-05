//go:build linux

package main

import (
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

func TestBuildCaptureRuleset(t *testing.T) {
	t.Run("one rule per port for an ipv4-only relay", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{80, 443}, selfAddrs{V4: "10.20.30.40"})
		require.NoError(t, err)
		assert.Equal(t, map[byte]int{unix.NFPROTO_INET: 2}, counts(),
			"one rule per port, tagged with the table's own inet family regardless of which L3 family the rule matches on")
	})

	t.Run("two rules per port for a dual-stack relay", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{443}, selfAddrs{V4: "10.20.30.40", V6: "fd00::5"})
		require.NoError(t, err)
		assert.Equal(t, map[byte]int{unix.NFPROTO_INET: 2}, counts(),
			"a v4 rule and a v6 rule for the one port")
	})

	t.Run("no rules when the relay has no address at all", func(t *testing.T) {
		conn, counts := newRuleCountingConn(t)

		err := buildCaptureRuleset(conn, []int{80, 443}, selfAddrs{})
		require.NoError(t, err)
		assert.Empty(t, counts())
	})
}

func TestCaptureRule(t *testing.T) {
	table := &nftables.Table{Family: nftables.TableFamilyINet, Name: captureTableName}
	chain := &nftables.Chain{Name: captureChainName, Table: table}

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
