package portforward

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/things-go/go-socks5/statute"
)

// udpForwardIdleWindow is how long a local sender stays eligible for a
// relay reply after it last sent to a UDP forward. SOCKS5 UDP ASSOCIATE
// only tracks one peer identity per session on the wire, so a forwarder
// can't demux replies by flow the way a real NAT would - instead it fans
// out every reply to every sender seen within this window, which handles
// the realistic case of two or three concurrent local tools (dig, a
// client library's retry, ...) sharing one forwarded port, instead of only
// the newest one working.
const udpForwardIdleWindow = 60 * time.Second

// UDP is one local loopback UDP listener that relays datagrams
// to a relay-routed UDP exposed port through a single SOCKS5 UDP ASSOCIATE
// session, held for the forward's whole life. Unlike TCP's
// per-connection dial, RFC 1928 ties an ASSOCIATE session's lifetime to its
// TCP control connection staying open, so Close here closes that
// connection explicitly rather than relying on the relay noticing an idle
// UDP socket - see docs/site/content/docs/concepts/relay.md. Create one with
// [StartUDP].
type UDP struct {
	pc      net.PacketConn // the local loopback listener
	control net.Conn       // the relay's SOCKS5 TCP control connection
	relay   net.Conn       // a connected UDP socket to the relay's pool port
	target  string         // the "<host>:<port>" this session's ASSOCIATE talks to

	mu      sync.Mutex
	closed  bool
	senders map[string]udpSender
}

// udpSender is one local address a UDP has recently seen a
// datagram from.
type udpSender struct {
	addr net.Addr
	seen time.Time
}

// StartUDP opens a loopback UDP listener for spec and establishes
// the SOCKS5 UDP ASSOCIATE session that carries its traffic. spec.Upstream
// must be a "socks5://<relay>/<target>" address, the same shape a relay
// entry's TCP forward uses; spec.RelayUDPAddrs must map the ASSOCIATE
// reply's pool port to a host-reachable address. Any handshake failure
// returns immediately, with no retry.
func StartUDP(ctx context.Context, spec Spec) (*UDP, error) {
	relayAddr, target, ok := SplitSOCKS5(spec.Upstream)
	if !ok {
		return nil, fmt.Errorf("portforward: %s: not a socks5:// upstream: %q", spec.Name, spec.Upstream)
	}

	var d net.Dialer
	control, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("portforward: %s: dial relay: %w", spec.Name, err)
	}

	relayUDPAddr, err := associate(control, spec.RelayUDPAddrs)
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("portforward: %s: socks5 udp associate: %w", spec.Name, err)
	}

	relayConn, err := d.DialContext(ctx, "udp", relayUDPAddr)
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("portforward: %s: dial relay udp pool port: %w", spec.Name, err)
	}

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(ctx, "udp", spec.listenAddr())
	if err != nil {
		_ = relayConn.Close()
		_ = control.Close()
		return nil, fmt.Errorf("portforward: %s: listen: %w", spec.Name, err)
	}

	f := &UDP{
		pc: pc, control: control, relay: relayConn, target: target,
		senders: make(map[string]udpSender),
	}
	// watchControl deliberately never uses ctx, the same reason
	// portForward.handle doesn't (see its comment): a forward must outlive
	// the DAG-walk-scoped context this function was given, long after Up
	// returns - the control connection closing, not ctx, is what ends it.
	go f.watchControl() //nolint:gosec,contextcheck // deliberate - see the comment above
	go f.forwardToRelay()
	go f.forwardFromRelay()
	return f, nil
}

// associate runs the SOCKS5 method negotiation and an ASSOCIATE request
// over control, then resolves the reply's bound port against
// relayUDPAddrs. x/net/proxy's SOCKS5 client only implements CONNECT, so
// this speaks the statute framing directly.
func associate(control net.Conn, relayUDPAddrs map[string]string) (string, error) {
	methodReq := statute.NewMethodRequest(statute.VersionSocks5, []byte{statute.MethodNoAuth})
	if _, err := control.Write(methodReq.Bytes()); err != nil {
		return "", fmt.Errorf("method negotiation: %w", err)
	}
	if _, err := statute.ParseMethodReply(control); err != nil {
		return "", fmt.Errorf("method negotiation: %w", err)
	}

	req := statute.Request{
		Version: statute.VersionSocks5, Command: statute.CommandAssociate,
		DstAddr: statute.AddrSpec{AddrType: statute.ATYPIPv4, IP: net.IPv4zero, Port: 0},
	}
	if _, err := control.Write(req.Bytes()); err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	rep, err := statute.ParseReply(control)
	if err != nil {
		return "", fmt.Errorf("read reply: %w", err)
	}
	if rep.Response != statute.RepSuccess {
		return "", fmt.Errorf("relay refused associate, code %d", rep.Response)
	}

	relayUDPAddr, ok := relayUDPAddrs[strconv.Itoa(rep.BndAddr.Port)]
	if !ok {
		return "", fmt.Errorf("relay has no published address for pool port %d", rep.BndAddr.Port)
	}
	return relayUDPAddr, nil
}

// watchControl blocks until the control connection closes - either the
// relay dropped it, or f.Close() closed it itself - then tears the rest of
// the session down. RFC 1928 ties an ASSOCIATE session's lifetime to this
// connection; there is no separate teardown message and no reconnect.
func (f *UDP) watchControl() {
	buf := make([]byte, 1)
	_, _ = f.control.Read(buf)
	if !f.markClosed() {
		return
	}
	log.Ctx(context.Background()).Warn("socks5 udp associate control connection closed", "target", f.target)
	_ = f.pc.Close()
	_ = f.relay.Close()
}

// markClosed reports f as closed and returns true the first time it is
// called - so Close and watchControl, which can race to tear the session
// down from either direction, only log and double-close once between them.
func (f *UDP) markClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.closed = true
	return true
}

// forwardToRelay reads datagrams from the local listener, tracks each
// sender, and relays the payload to target through the ASSOCIATE session,
// wrapped in the SOCKS5 UDP header. Returns once the local listener closes.
func (f *UDP) forwardToRelay() {
	buf := make([]byte, 64*1024)
	for {
		n, addr, err := f.pc.ReadFrom(buf)
		if err != nil {
			return
		}
		f.trackSender(addr)

		dgram, err := statute.NewDatagram(f.target, buf[:n])
		if err != nil {
			continue
		}
		if _, err := f.relay.Write(dgram.Bytes()); err != nil {
			return
		}
	}
}

// forwardFromRelay reads reply datagrams from the ASSOCIATE session and
// fans each one's payload out to every local sender seen within
// udpForwardIdleWindow. Returns once the relay connection closes.
func (f *UDP) forwardFromRelay() {
	buf := make([]byte, 64*1024)
	for {
		n, err := f.relay.Read(buf)
		if err != nil {
			return
		}
		dgram, err := statute.ParseDatagram(buf[:n])
		if err != nil {
			continue
		}
		for _, addr := range f.activeSenders() {
			_, _ = f.pc.WriteTo(dgram.Data, addr)
		}
	}
}

// trackSender records addr as having just sent a datagram.
func (f *UDP) trackSender(addr net.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.senders[addr.String()] = udpSender{addr: addr, seen: time.Now()}
}

// activeSenders returns every sender seen within udpForwardIdleWindow,
// pruning the rest.
func (f *UDP) activeSenders() []net.Addr {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	addrs := make([]net.Addr, 0, len(f.senders))
	for key, s := range f.senders {
		if now.Sub(s.seen) > udpForwardIdleWindow {
			delete(f.senders, key)
			continue
		}
		addrs = append(addrs, s.addr)
	}
	return addrs
}

// Addr is the address of the local loopback listener.
func (f *UDP) Addr() net.Addr { return f.pc.LocalAddr() }

// Close ends the ASSOCIATE session and stops the local listener. Close is
// idempotent with watchControl noticing the same closure from the other
// direction.
func (f *UDP) Close() error {
	if !f.markClosed() {
		return nil
	}
	return errors.Join(f.pc.Close(), f.relay.Close(), f.control.Close())
}
