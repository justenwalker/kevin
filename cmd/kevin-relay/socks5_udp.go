package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/things-go/go-socks5"
	"github.com/things-go/go-socks5/statute"
)

// udpPool hands out one of a fixed set of pre-published UDP ports to each
// ASSOCIATE session. A session's bound port must already be published by
// Docker or kind before this process started (see internal/relay's own
// pool, KEVIN_RELAY_UDP_POOL_SIZE) - the library's own handleAssociate asks
// the OS for an ephemeral port instead, which can't be pre-published.
type udpPool struct {
	mu   sync.Mutex
	free []int
}

// newUDPPool returns a pool that hands out each of ports exactly once at a
// time. An empty ports gives a pool with no UDP ASSOCIATE capacity at all -
// every acquire fails.
func newUDPPool(ports []int) *udpPool {
	free := make([]int, len(ports))
	copy(free, ports)
	return &udpPool{free: free}
}

// acquire removes and returns one free port. ok is false when the pool is
// empty.
func (p *udpPool) acquire() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.free) == 0 {
		return 0, false
	}
	n := len(p.free) - 1
	port := p.free[n]
	p.free = p.free[:n]
	return port, true
}

// release returns port to the pool.
func (p *udpPool) release(port int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.free = append(p.free, port)
}

// parseUDPRelayPorts parses a "<start>-<end>" inclusive port range, the
// form internal/relay and internal/plugins/kind pass via --udp-relay-ports,
// into the ports it names. An empty s returns no ports: kevin-relay then
// has no UDP ASSOCIATE capacity, the right default for a direct or manual
// invocation and for a test that doesn't care about it.
func parseUDPRelayPorts(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	start, end, ok := strings.Cut(s, "-")
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidUDPRelayPorts, s)
	}
	startN, startErr := strconv.Atoi(start)
	endN, endErr := strconv.Atoi(end)
	if startErr != nil || endErr != nil || endN < startN {
		return nil, fmt.Errorf("%w: %q", ErrInvalidUDPRelayPorts, s)
	}
	ports := make([]int, 0, endN-startN+1)
	for p := startN; p <= endN; p++ {
		ports = append(ports, p)
	}
	return ports, nil
}

// newAssociateHandler returns a socks5.Handler implementing SOCKS5 UDP
// ASSOCIATE (RFC 1928 §7) by binding a port from pool instead of asking the
// OS for an ephemeral one, since Docker/kind need the port fixed before the
// container/pod that publishes it exists. It replies RepServerFailure
// immediately when the pool is exhausted, with no wait for a slot to free
// up, and releases its port as soon as the client's TCP control connection
// closes, tearing the session's UDP listener down with it - the same
// lifetime rule RFC 1928 already ties an association to.
func newAssociateHandler(pool *udpPool) socks5.Handler {
	return func(ctx context.Context, writer io.Writer, request *socks5.Request) error {
		port, ok := pool.acquire()
		if !ok {
			if err := socks5.SendReply(writer, statute.RepServerFailure, nil); err != nil {
				return fmt.Errorf("relay: socks5 associate: send reply: %w", err)
			}
			return fmt.Errorf("relay: socks5 associate: %w", ErrUDPPoolExhausted)
		}
		defer pool.release(port)

		bindLn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			if sendErr := socks5.SendReply(writer, statute.RepServerFailure, nil); sendErr != nil {
				return fmt.Errorf("relay: socks5 associate: send reply: %w", sendErr)
			}
			return fmt.Errorf("relay: socks5 associate: listen udp %d: %w", port, err)
		}
		defer bindLn.Close() //nolint:errcheck // best-effort; the control-read loop below also closes it

		if err := socks5.SendReply(writer, statute.RepSuccess, bindLn.LocalAddr()); err != nil {
			return fmt.Errorf("relay: socks5 associate: send reply: %w", err)
		}

		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			relayUDPDatagrams(ctx, bindLn)
		}()

		// The control connection's lifetime is the association's lifetime
		// (RFC 1928 §7): closing it tears the UDP session down. This
		// handler serves one caller for its whole life, so it skips the
		// library's own per-datagram source-address check against the
		// ASSOCIATE request's declared DestAddr.
		buf := make([]byte, 1)
		_, _ = request.Reader.Read(buf)
		_ = bindLn.Close() // unblocks relayUDPDatagrams's ReadFromUDP
		<-relayDone
		return nil
	}
}

// relayUDPDatagrams reads SOCKS5 UDP datagrams from bindLn, keyed on the
// destination each one names, dialing that destination on first use and
// relaying its replies back. Mirrors the vendored library's own
// handleAssociate, adapted to run over a pre-bound pool port instead of an
// ephemeral one. Returns once bindLn is closed.
func relayUDPDatagrams(ctx context.Context, bindLn *net.UDPConn) {
	var conns sync.Map
	defer func() {
		conns.Range(func(_, value any) bool {
			if conn, ok := value.(net.Conn); ok {
				_ = conn.Close()
			}
			return true
		})
	}()

	var dialer net.Dialer
	buf := make([]byte, 64*1024)
	for {
		n, srcAddr, err := bindLn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		pk, err := statute.ParseDatagram(buf[:n])
		if err != nil {
			continue
		}

		connKey := srcAddr.String() + "--" + pk.DstAddr.String()
		var conn net.Conn
		if cached, loaded := conns.Load(connKey); loaded {
			c, ok := cached.(net.Conn)
			if !ok {
				continue
			}
			conn = c
		} else {
			dialed, dialErr := dialer.DialContext(ctx, "udp", pk.DstAddr.String())
			if dialErr != nil {
				continue
			}
			conns.Store(connKey, dialed)
			go relayUDPReplies(bindLn, dialed, srcAddr, pk, &conns, connKey)
			conn = dialed
		}
		if _, err := conn.Write(pk.Data); err != nil {
			continue
		}
	}
}

// relayUDPReplies reads target's replies and writes each one back to
// srcAddr through bindLn, wrapped in pk's own SOCKS5 UDP header, until
// target's connection ends.
func relayUDPReplies(bindLn *net.UDPConn, target net.Conn, srcAddr *net.UDPAddr, pk statute.Datagram, conns *sync.Map, connKey string) {
	defer func() {
		_ = target.Close()
		conns.Delete(connKey)
	}()

	buf := make([]byte, 64*1024)
	for {
		n, err := target.Read(buf)
		if err != nil {
			return
		}
		if _, err := bindLn.WriteTo(append(pk.Header(), buf[:n]...), srcAddr); err != nil {
			return
		}
	}
}
