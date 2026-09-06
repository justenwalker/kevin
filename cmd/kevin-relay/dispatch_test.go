package main

import (
	"bufio"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDispatchTunnelsOnFakeIPMatch covers the match branch: lookupOrigDst is
// swapped for a stub so the test doesn't need a real DNAT'd connection (only
// origDst itself, not dispatch's own routing logic, needs the real kernel -
// see TestSwapPort and this session's manual verification against a real
// relay container for that half), and proves dispatch tunnels straight to
// the proxy for the resolved host with no protocol parsing, never calling
// fallback.
func TestDispatchTunnelsOnFakeIPMatch(t *testing.T) {
	pool, err := newFakeIPPool("198.18.0.0/15", "100::/64")
	require.NoError(t, err)

	const host = "raw.example.com"
	addrs, err := pool.allocate(host)
	require.NoError(t, err)
	v4, err := netip.ParseAddr(addrs.V4)
	require.NoError(t, err)

	orig := lookupOrigDst
	t.Cleanup(func() { lookupOrigDst = orig })
	lookupOrigDst = func(net.Conn) (netip.AddrPort, error) {
		return netip.AddrPortFrom(v4, 9999), nil
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	connectLine := make(chan string, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		br := bufio.NewReader(conn)
		line, readErr := br.ReadString('\n')
		if readErr != nil {
			return
		}
		connectLine <- line
		for {
			h, hErr := br.ReadString('\n')
			if hErr != nil || h == "\r\n" {
				break
			}
		}
		if _, writeErr := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); writeErr != nil {
			return
		}
		buf := make([]byte, 4096)
		n, _ := br.Read(buf)
		if n > 0 {
			_, _ = conn.Write(buf[:n])
		}
	}()

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	dispatchDone := make(chan struct{})
	go func() {
		dispatch(t.Context(), server, ln.Addr().String(), 9999, pool, func(net.Conn) {
			t.Error("fallback must not run for a fake-IP match")
		})
		close(dispatchDone)
	}()

	select {
	case line := <-connectLine:
		assert.Equal(t, "CONNECT raw.example.com:9999 HTTP/1.1\r\n", line,
			"the recovered host, not the fake IP, must reach the proxy - no SNI/Host parsing needed")
	case <-time.After(2 * time.Second):
		t.Fatal("the stub proxy never saw a CONNECT request")
	}

	_, err = client.Write([]byte("raw payload"))
	require.NoError(t, err)
	buf := make([]byte, len("raw payload"))
	_, err = client.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "raw payload", string(buf), "bytes must pipe through with no protocol assumption")

	_ = client.Close()
	<-dispatchDone
}

// TestDispatchFallsThroughWithNoFakeIPMatch covers the case every platform
// can exercise without a real DNAT'd connection: origDst either errors (a
// non-linux build, or a connection that was never captured) or names an
// address the pool never allocated, and either way dispatch must run
// fallback, not silently drop the connection. The "does match" branch is
// TestDispatchTunnelsOnFakeIPMatch, above.
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
