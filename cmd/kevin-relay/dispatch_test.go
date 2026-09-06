package main

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchFallsThroughWithNoFakeIPMatch covers the case every platform
// can exercise without a real DNAT'd connection: origDst either errors (a
// non-linux build, or a connection that was never captured) or names an
// address the pool never allocated, and either way dispatch must run
// fallback, not silently drop the connection. The "does match" branch
// needs a real captured connection - see the kevin-relay integration
// suite's fake-IP test.
func TestDispatchFallsThroughWithNoFakeIPMatch(t *testing.T) {
	pool, err := newFakeIPPool("198.18.0.0/15", "100::/64")
	require.NoError(t, err)

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go func() { _ = server.Close() }()

	called := make(chan net.Conn, 1)
	dispatch(t.Context(), client, "127.0.0.1:0", 443, pool, func(c net.Conn) {
		called <- c
	})

	select {
	case c := <-called:
		assert.Same(t, client, c, "fallback must receive the original connection")
	default:
		t.Fatal("fallback was never called")
	}
}
