package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/protos/pb"
)

func TestApplyFault(t *testing.T) {
	t.Run("records nothing when the fault cannot be applied", func(t *testing.T) {
		p := &relayProcess{}

		_, err := p.ApplyFault(t.Context(), &pb.ApplyFaultRequest{Id: "db", NetnsPath: "/nonexistent/netns", Interface: "eth0"})

		require.ErrorContains(t, err, `apply fault for "db"`)
		assert.Empty(t, p.faults)
	})
}

func TestClearFault(t *testing.T) {
	t.Run("is a no-op for a fault that was never applied", func(t *testing.T) {
		p := &relayProcess{}

		_, err := p.ClearFault(t.Context(), &pb.ClearFaultRequest{Id: "db"})

		require.NoError(t, err)
	})

	t.Run("forgets a fault even when its namespace is gone", func(t *testing.T) {
		p := &relayProcess{faults: map[string]faultTarget{"db": {netnsPath: "/nonexistent/netns", iface: "eth0"}}}

		_, err := p.ClearFault(t.Context(), &pb.ClearFaultRequest{Id: "db"})

		require.NoError(t, err)
		assert.Empty(t, p.faults)
	})
}
