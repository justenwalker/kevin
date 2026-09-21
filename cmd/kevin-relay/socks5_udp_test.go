package main

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

func TestUDPPool(t *testing.T) {
	t.Run("hands out each port once and reports exhaustion", func(t *testing.T) {
		pool := newUDPPool([]int{40000, 40001})

		first, ok := pool.acquire()
		require.True(t, ok)
		second, ok := pool.acquire()
		require.True(t, ok)
		assert.NotEqual(t, first, second)

		_, ok = pool.acquire()
		assert.False(t, ok, "a third acquire must fail once both ports are taken")
	})

	t.Run("release makes a port available again", func(t *testing.T) {
		pool := newUDPPool([]int{40000})

		port, ok := pool.acquire()
		require.True(t, ok)
		pool.release(port)

		got, ok := pool.acquire()
		require.True(t, ok)
		assert.Equal(t, port, got)
	})

	t.Run("an empty pool has no capacity", func(t *testing.T) {
		pool := newUDPPool(nil)
		_, ok := pool.acquire()
		assert.False(t, ok)
	})
}

func TestParseUDPRelayPorts(t *testing.T) {
	tests := []struct {
		name    string
		give    string
		want    []int
		wantErr bool
	}{
		{name: "empty means no capacity", give: "", want: nil},
		{name: "a single-port range", give: "40000-40000", want: []int{40000}},
		{name: "a multi-port range", give: "40000-40002", want: []int{40000, 40001, 40002}},
		{name: "missing the separator is an error", give: "40000", wantErr: true},
		{name: "a non-numeric bound is an error", give: "abc-40002", wantErr: true},
		{name: "an inverted range is an error", give: "40002-40000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUDPRelayPorts(tt.give)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidUDPRelayPorts)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// newTestAssociateServer starts a SOCKS5 server on loopback with an
// associate handler backed by a pool of poolPorts, returning its address.
func newTestAssociateServer(t *testing.T, poolPorts []int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test fixture, no cancellation needed
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	srv := socks5.NewServer(socks5.WithAssociateHandle(newAssociateHandler(newUDPPool(poolPorts))))
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String()
}

// associate dials addr, negotiates no-auth, and sends an ASSOCIATE request,
// returning the open control connection and the parsed reply. The caller
// closes conn to end the association.
func associate(t *testing.T, addr string) (net.Conn, statute.Reply) {
	t.Helper()
	conn, err := net.Dial("tcp", addr) //nolint:noctx // test fixture, no cancellation needed
	require.NoError(t, err)

	_, err = conn.Write(statute.NewMethodRequest(statute.VersionSocks5, []byte{statute.MethodNoAuth}).Bytes())
	require.NoError(t, err)
	_, err = statute.ParseMethodReply(conn)
	require.NoError(t, err)

	req := statute.Request{
		Version: statute.VersionSocks5, Command: statute.CommandAssociate,
		DstAddr: statute.AddrSpec{AddrType: statute.ATYPIPv4, IP: net.IPv4zero, Port: 0},
	}
	_, err = conn.Write(req.Bytes())
	require.NoError(t, err)

	rep, err := statute.ParseReply(conn)
	require.NoError(t, err)
	return conn, rep
}

// newUDPEcho starts a UDP echo listener on loopback and returns its address.
func newUDPEcho(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0") //nolint:noctx // test fixture, no cancellation needed
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			if _, err := conn.WriteTo(buf[:n], addr); err != nil {
				return
			}
		}
	}()
	return conn.LocalAddr().String()
}

func TestAssociateHandler(t *testing.T) {
	t.Run("round trips a datagram through the pool port", func(t *testing.T) {
		addr := newTestAssociateServer(t, []int{40100})
		echoAddr := newUDPEcho(t)

		conn, rep := associate(t, addr)
		defer func() { _ = conn.Close() }()
		require.Equal(t, statute.RepSuccess, rep.Response)
		assert.Equal(t, 40100, rep.BndAddr.Port, "the reply must name the pool's only port")

		client, err := net.Dial("udp", net.JoinHostPort("127.0.0.1", "40100")) //nolint:noctx // test fixture
		require.NoError(t, err)
		defer func() { _ = client.Close() }()

		dgram, err := statute.NewDatagram(echoAddr, []byte("hello"))
		require.NoError(t, err)
		_, err = client.Write(dgram.Bytes())
		require.NoError(t, err)

		require.NoError(t, client.SetReadDeadline(time.Now().Add(5*time.Second)))
		buf := make([]byte, 64*1024)
		n, err := client.Read(buf)
		require.NoError(t, err)

		reply, err := statute.ParseDatagram(buf[:n])
		require.NoError(t, err)
		assert.Equal(t, "hello", string(reply.Data))
	})

	t.Run("replies RepServerFailure immediately when the pool is exhausted", func(t *testing.T) {
		addr := newTestAssociateServer(t, []int{40101})

		first, rep := associate(t, addr)
		defer func() { _ = first.Close() }()
		require.Equal(t, statute.RepSuccess, rep.Response)

		second, rep := associate(t, addr)
		defer func() { _ = second.Close() }()
		assert.Equal(t, statute.RepServerFailure, rep.Response)
	})

	t.Run("closing the control connection releases the pool port", func(t *testing.T) {
		addr := newTestAssociateServer(t, []int{40102})

		first, rep := associate(t, addr)
		require.Equal(t, statute.RepSuccess, rep.Response)
		require.NoError(t, first.Close())

		require.Eventually(t, func() bool {
			second, rep := associate(t, addr)
			defer func() { _ = second.Close() }()
			return rep.Response == statute.RepSuccess
		}, 2*time.Second, 20*time.Millisecond, "the port must free up once the first control connection closes")
	})
}
