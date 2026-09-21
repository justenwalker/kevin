package engine

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"

	"github.com/justenwalker/kevin/protos/pb"
)

// newUDPEcho starts a UDP echo listener on loopback and returns its
// address.
func newUDPEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx // test fixture, no cancellation needed
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}
			if _, writeErr := conn.WriteTo(buf[:n], addr); writeErr != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

// findFreeUDPPort asks the OS for a free loopback UDP port and returns its
// number, for a fixture that needs to bind a specific, pre-chosen port the
// way kevin-relay's real pool does.
func findFreeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	require.True(t, ok)
	require.NoError(t, conn.Close())
	return addr.Port
}

// newFakeUDPRelay starts a real SOCKS5 server, same construction as
// cmd/kevin-relay/socks5.go, whose ASSOCIATE handler binds a single
// pre-chosen pool port instead of an OS-assigned one - kevin-relay's own
// pool-backed handler isn't importable here (cmd/kevin-relay is package
// main), so this reimplements just enough of it for a test fixture: one
// session, one destination, no pool exhaustion. Returns the server's
// address and the pool port its handler binds.
func newFakeUDPRelay(t *testing.T) (string, int) {
	t.Helper()
	poolPort := findFreeUDPPort(t)

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	srv := socks5.NewServer(socks5.WithAssociateHandle(fakeUDPAssociateHandler(poolPort)))
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String(), poolPort
}

// fakeUDPAssociateHandler binds bindPort exactly and relays datagrams to
// whatever single destination the session's first datagram names, the same
// shape as kevin-relay's real pool-backed handler but without pool
// bookkeeping or multi-destination support - this test only ever needs one
// of each.
func fakeUDPAssociateHandler(bindPort int) socks5.Handler {
	return func(_ context.Context, writer io.Writer, request *socks5.Request) error {
		bindLn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: bindPort})
		if err != nil {
			return err
		}
		defer func() { _ = bindLn.Close() }()

		if err := socks5.SendReply(writer, statute.RepSuccess, bindLn.LocalAddr()); err != nil {
			return err
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			relayOneDestination(bindLn)
		}()

		buf := make([]byte, 1)
		_, _ = request.Reader.Read(buf)
		_ = bindLn.Close()
		<-done
		return nil
	}
}

// relayOneDestination reads datagrams from bindLn, dials the first one's
// destination, and relays every subsequent datagram to it, writing each
// reply back wrapped in the first datagram's own header - fine for a test
// fixture since every case here targets one destination per session.
func relayOneDestination(bindLn *net.UDPConn) {
	buf := make([]byte, 64*1024)
	n, srcAddr, err := bindLn.ReadFromUDP(buf)
	if err != nil {
		return
	}
	pk, err := statute.ParseDatagram(buf[:n])
	if err != nil {
		return
	}
	target, err := net.Dial("udp", pk.DstAddr.String()) //nolint:noctx // test fixture
	if err != nil {
		return
	}
	defer func() { _ = target.Close() }()
	if _, err := target.Write(pk.Data); err != nil {
		return
	}

	go func() {
		rbuf := make([]byte, 64*1024)
		for {
			n, readErr := target.Read(rbuf)
			if readErr != nil {
				return
			}
			if _, writeErr := bindLn.WriteTo(append(pk.Header(), rbuf[:n]...), srcAddr); writeErr != nil {
				return
			}
		}
	}()

	for {
		n, _, readErr := bindLn.ReadFromUDP(buf)
		if readErr != nil {
			return
		}
		next, parseErr := statute.ParseDatagram(buf[:n])
		if parseErr != nil {
			continue
		}
		if _, writeErr := target.Write(next.Data); writeErr != nil {
			return
		}
	}
}

// newUDPRelayExposedPort builds an ExposedPort naming a relay-routed udp
// entry through relayAddr/poolPort at echoAddr - the shape container's or
// kind's own exposedPorts()/exposedViaRelay() report.
func newUDPRelayExposedPort(relayAddr string, poolPort int, echoAddr string) *pb.ExposedPort {
	return &pb.ExposedPort{
		Name: "dns", Protocol: "udp", Relay: true,
		Upstream:      fmt.Sprintf("socks5://%s/%s", relayAddr, echoAddr),
		RelayUdpAddrs: map[string]string{strconv.Itoa(poolPort): fmt.Sprintf("127.0.0.1:%d", poolPort)},
	}
}

func TestNewUDPPortForward(t *testing.T) {
	t.Run("round trips a datagram through the associate session", func(t *testing.T) {
		relayAddr, poolPort := newFakeUDPRelay(t)
		echoAddr := newUDPEcho(t)

		ep := newUDPRelayExposedPort(relayAddr, poolPort, echoAddr)
		pf, err := newUDPPortForward(t.Context(), ep)
		require.NoError(t, err)
		t.Cleanup(func() { _ = pf.Close() })

		var d net.Dialer
		client, err := d.DialContext(t.Context(), "udp", pf.Addr().String())
		require.NoError(t, err)
		defer func() { _ = client.Close() }()

		_, err = client.Write([]byte("ping"))
		require.NoError(t, err)

		require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
		buf := make([]byte, 4)
		_, err = io.ReadFull(client, buf)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(buf))
	})

	t.Run("a malformed upstream errors", func(t *testing.T) {
		ep := &pb.ExposedPort{Name: "dns", Protocol: "udp", Relay: true, Upstream: "127.0.0.1:53"}
		pf, err := newUDPPortForward(t.Context(), ep)
		require.Error(t, err)
		assert.Nil(t, pf)
	})

	t.Run("an unknown pool port errors immediately", func(t *testing.T) {
		relayAddr, poolPort := newFakeUDPRelay(t)
		echoAddr := newUDPEcho(t)

		ep := newUDPRelayExposedPort(relayAddr, poolPort, echoAddr)
		ep.RelayUdpAddrs = map[string]string{"1": "127.0.0.1:1"} // wrong key - the real pool port is poolPort

		pf, err := newUDPPortForward(t.Context(), ep)
		require.Error(t, err)
		assert.Nil(t, pf)
	})

	t.Run("a dropped control connection closes the session", func(t *testing.T) {
		relayAddr, poolPort := newFakeUDPRelay(t)
		echoAddr := newUDPEcho(t)

		ep := newUDPRelayExposedPort(relayAddr, poolPort, echoAddr)
		f, err := newUDPPortForward(t.Context(), ep)
		require.NoError(t, err)
		t.Cleanup(func() { _ = f.Close() })

		require.NoError(t, f.control.Close())

		require.Eventually(t, func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.closed
		}, 2*time.Second, 20*time.Millisecond, "watchControl must notice the dropped connection and close the session")
	})

	t.Run("fans out a reply to every recently active local sender", func(t *testing.T) {
		relayAddr, poolPort := newFakeUDPRelay(t)
		echoAddr := newUDPEcho(t)

		ep := newUDPRelayExposedPort(relayAddr, poolPort, echoAddr)
		pf, err := newUDPPortForward(t.Context(), ep)
		require.NoError(t, err)
		t.Cleanup(func() { _ = pf.Close() })

		var d net.Dialer
		a, err := d.DialContext(t.Context(), "udp", pf.Addr().String())
		require.NoError(t, err)
		defer func() { _ = a.Close() }()
		b, err := d.DialContext(t.Context(), "udp", pf.Addr().String())
		require.NoError(t, err)
		defer func() { _ = b.Close() }()

		// b sends once, purely to register as a recently active sender,
		// and drains its own reply before the real assertion below.
		_, err = b.Write([]byte("hi"))
		require.NoError(t, err)
		require.NoError(t, b.SetReadDeadline(time.Now().Add(5*time.Second)))
		bDrain := make([]byte, 2)
		_, err = io.ReadFull(b, bDrain)
		require.NoError(t, err)

		_, err = a.Write([]byte("ping"))
		require.NoError(t, err)

		require.NoError(t, a.SetReadDeadline(time.Now().Add(5*time.Second)))
		bufA := make([]byte, 4)
		_, err = io.ReadFull(a, bufA)
		require.NoError(t, err)
		assert.Equal(t, "ping", string(bufA))

		require.NoError(t, b.SetReadDeadline(time.Now().Add(5*time.Second)))
		bufB := make([]byte, 4)
		_, err = io.ReadFull(b, bufB)
		require.NoError(t, err, "b must also receive a's reply - the forwarder fans out to every recent sender, not just the one that asked")
		assert.Equal(t, "ping", string(bufB))
	})
}
