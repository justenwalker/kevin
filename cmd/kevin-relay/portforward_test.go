package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func listenUDP(t *testing.T) net.PacketConn {
	t.Helper()
	var lc net.ListenConfig
	pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

func dial(t *testing.T, network, addr string) net.Conn {
	t.Helper()
	var d net.Dialer
	c, err := d.DialContext(t.Context(), network, addr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
	return c
}

func TestForwardTCP(t *testing.T) {
	t.Run("copies both ways to the target", func(t *testing.T) {
		echo := listenTCP(t)
		go func() {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			defer c.Close() //nolint:errcheck // test helper
			_, _ = io.Copy(c, c)
		}()

		ln := listenTCP(t)
		go func() { _ = forwardTCP(t.Context(), ln, echo.Addr().String()) }()

		c := dial(t, "tcp", ln.Addr().String())
		_, err := c.Write([]byte("ping"))
		require.NoError(t, err)

		got := make([]byte, 4)
		_, err = io.ReadFull(c, got)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(got))
	})
}

func TestForwardUDP(t *testing.T) {
	t.Run("returns each client's reply to that client", func(t *testing.T) {
		echo := listenUDP(t)
		go func() {
			buf := make([]byte, 1024)
			for {
				n, from, readErr := echo.ReadFrom(buf)
				if readErr != nil {
					return
				}
				_, _ = echo.WriteTo(buf[:n], from)
			}
		}()

		fwd := listenUDP(t)
		go func() { _ = forwardUDP(t.Context(), fwd, echo.LocalAddr().String()) }()

		for _, msg := range []string{"one", "two"} {
			c := dial(t, "udp", fwd.LocalAddr().String())
			_, err := c.Write([]byte(msg))
			require.NoError(t, err)

			buf := make([]byte, 16)
			n, err := c.Read(buf)
			require.NoError(t, err)
			assert.Equal(t, msg, string(buf[:n]))
		}
	})
}

func TestPortForwardCommand(t *testing.T) {
	t.Run("requires target and tcp", func(t *testing.T) {
		cmd := portForwardCommand()
		cmd.SetArgs(nil)
		assert.Error(t, cmd.Execute())
	})

	t.Run("rejects a bad udp port range", func(t *testing.T) {
		cmd := portForwardCommand()
		cmd.SetArgs([]string{"--target", "x", "--tcp", "1080", "--udp-relay-ports", "bad"})
		assert.ErrorIs(t, cmd.Execute(), ErrInvalidUDPRelayPorts)
	})
}
