//go:build linux

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFaultInterface covers the one piece of netfault_linux.go that's
// pure and kernel-independent. buildFaultQdisc/applyFault/clearFault
// themselves aren't unit-tested here: unlike google/nftables (which
// exposes nftables.WithTestDial, letting netcapture_linux_test.go inspect
// the messages a call would send with no kernel involved),
// vishvananda/netlink's Handle always opens a real NETLINK_ROUTE socket -
// there's no equivalent fake-dial injection point. netcapture_linux.go
// itself doesn't unit-test its own real success path either, only its
// failure paths plus a documented "verified empirically against a real
// container" note - this file follows that same precedent rather than
// root-gating a real-netns test for marginal value. See
// cmd/kevin-relay/integration_test.go for the failure-path coverage that
// doesn't need a kernel, and AGENTS.md's manual-testing notes for the
// real end-to-end check.
func TestFaultInterface(t *testing.T) {
	t.Run("empty defaults to eth0", func(t *testing.T) {
		assert.Equal(t, defaultFaultInterface, faultInterface(""))
	})
	t.Run("non-empty passes through", func(t *testing.T) {
		assert.Equal(t, "eth1", faultInterface("eth1"))
	})
}
