package engine

import (
	"bytes"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/run/session"
	"github.com/justenwalker/kevin/internal/step"
)

func TestExposePort(t *testing.T) {
	// busyPort is a loopback port that stays bound for the test, so a TCP
	// forward pinned to it cannot listen.
	busyPort := func(t *testing.T) int {
		t.Helper()
		var lc net.ListenConfig
		ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })
		addr, ok := ln.Addr().(*net.TCPAddr)
		require.True(t, ok)
		return addr.Port
	}
	// A relay nothing listens on makes a UDP forward fail while it dials.
	newEP := func(protocol step.Protocol, hostPort int) step.ExposedPort {
		return step.ExposedPort{
			Name: "u", Protocol: protocol, HostPort: hostPort,
			Upstream: step.Upstream{Relay: &step.RelayTarget{Relay: "127.0.0.1:1", Target: "svc:80"}},
		}
	}

	t.Run("a failed UDP forward is an error", func(t *testing.T) {
		var events bytes.Buffer
		r := &run{events: &events}
		err := r.exposePort(t.Context(), "udp", newEP(step.UDP, 0), output.Map{})
		require.ErrorContains(t, err, "local forward for u")
	})

	t.Run("a failed TCP forward is only a warning", func(t *testing.T) {
		var events bytes.Buffer
		r := &run{events: &events}
		require.NoError(t, r.exposePort(t.Context(), "web", newEP(step.TCP, busyPort(t)), output.Map{}))
		assert.Contains(t, events.String(), "warning: local forward for u")
	})

	t.Run("a direct upstream opens no forward", func(t *testing.T) {
		var events bytes.Buffer
		r := &run{events: &events}
		ep := step.ExposedPort{Name: "db", Upstream: step.Upstream{Addr: "127.0.0.1:5432"}}
		out := output.Map{}

		require.NoError(t, r.exposePort(t.Context(), "db", ep, out))

		assert.Equal(t, output.Map{"expose_db": {String: "127.0.0.1:5432"}}, out)
	})

	t.Run("exposePorts marks the step failed", func(t *testing.T) {
		var events bytes.Buffer
		store := session.NewStore()
		r := &run{events: &events, store: store}
		eps := []step.ExposedPort{newEP(step.UDP, 0)}
		require.Error(t, r.exposePorts(t.Context(), "udp", eps, output.Map{}))
		assert.Contains(t, events.String(), "failed: ")
	})
}
