package portforward

import (
	"io"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/things-go/go-socks5"
)

func TestStartTCP(t *testing.T) {
	t.Run("pipes both directions", func(t *testing.T) {
		var lc net.ListenConfig

		// fake target: echoes whatever it reads.
		target, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = target.Close() }()
		go func() {
			conn, acceptErr := target.Accept()
			if acceptErr != nil {
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(conn, conn)
		}()

		// fake relay: a real SOCKS5 server, same construction as
		// cmd/kevin-relay/socks5.go.
		relay, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = relay.Close() }()
		go func() { _ = socks5.NewServer().Serve(relay) }()

		ep := Spec{
			Name:   "db",
			Relay:  relay.Addr().String(),
			Target: target.Addr().String(),
		}
		pf, err := StartTCP(t.Context(), ep)
		require.NoError(t, err)
		t.Cleanup(func() { _ = pf.Close() })

		var d net.Dialer
		conn, err := d.DialContext(t.Context(), "tcp", pf.Addr().String())
		require.NoError(t, err)
		defer func() { _ = conn.Close() }()

		_, err = conn.Write([]byte("ping"))
		require.NoError(t, err)

		buf := make([]byte, 4)
		_, err = io.ReadFull(conn, buf)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(buf))
	})

	t.Run("host_port pins the local listener", func(t *testing.T) {
		var lc net.ListenConfig

		relay, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer func() { _ = relay.Close() }()
		go func() { _ = socks5.NewServer().Serve(relay) }()

		// Reserve a free port, then close it - StartTCP must rebind it.
		reserved, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		reservedAddr, ok := reserved.Addr().(*net.TCPAddr)
		require.True(t, ok)
		hostPort := reservedAddr.Port
		require.NoError(t, reserved.Close())

		ep := Spec{
			Name:     "db",
			Relay:    relay.Addr().String(),
			Target:   "127.0.0.1:1",
			HostPort: hostPort,
		}
		pf, err := StartTCP(t.Context(), ep)
		require.NoError(t, err)
		t.Cleanup(func() { _ = pf.Close() })

		pfAddr, ok := pf.Addr().(*net.TCPAddr)
		require.True(t, ok)
		assert.Equal(t, hostPort, pfAddr.Port)
	})
}
