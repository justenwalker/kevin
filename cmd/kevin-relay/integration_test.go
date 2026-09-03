//go:build integration

package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"golang.org/x/net/proxy"
)

// relayTestDomain is the environment domain that the suite's relay answers
// for.
const relayTestDomain = "kevin.home"

// RelayProcessSuite drives the relay's own servers in process, on ephemeral
// ports, with no docker daemon involved.
type RelayProcessSuite struct {
	suite.Suite

	cancel context.CancelFunc
	done   chan error

	proc       *relayProcess
	upstream   *dns.Server
	upstreamPC net.PacketConn

	proxyStub  *httptest.Server
	proxyLines chan string
}

func TestRelayProcessSuite(t *testing.T) {
	suite.Run(t, new(RelayProcessSuite))
}

// SetupSuite starts a fake upstream DNS resolver, a stub HTTP proxy, a stub
// CONNECT proxy, and the relay's own servers, all on ephemeral ports.
func (s *RelayProcessSuite) SetupSuite() {
	t := s.T()

	s.upstreamPC, s.upstream = startUpstreamDNS(t)

	s.proxyLines = make(chan string, 4)
	s.proxyStub = httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		s.proxyLines <- r.Method + " " + r.RequestURI + " " + r.Proto
	}))

	proc, err := newRelayProcess(t.Context(), config{
		domain:       relayTestDomain,
		proxyAddr:    s.proxyStub.Listener.Addr().String(),
		self:         "10.20.30.40",
		dnsListen:    "127.0.0.1:0",
		httpListen:   "127.0.0.1:0",
		httpsListen:  "127.0.0.1:0",
		socks5Listen: "127.0.0.1:0",
		upstreamDNS:  s.upstreamPC.LocalAddr().String(),
	})
	s.Require().NoError(err)
	s.proc = proc

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	go func() { s.done <- proc.run(ctx) }()
}

// TearDownSuite stops every server that SetupSuite started.
func (s *RelayProcessSuite) TearDownSuite() {
	if s.cancel != nil {
		s.cancel()
		s.Require().NoError(<-s.done)
	}
	if s.upstream != nil {
		_ = s.upstream.ShutdownContext(context.Background())
	}
	if s.proxyStub != nil {
		s.proxyStub.Close()
	}
}

// startUpstreamDNS runs a minimal DNS server that answers every A query with
// a fixed address, and returns its packet connection and server.
func startUpstreamDNS(t *testing.T) (net.PacketConn, *dns.Server) {
	t.Helper()

	var lc net.ListenConfig
	pc, err := lc.ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)

	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		rr, rrErr := dns.NewRR(req.Question[0].Name + " 30 IN A 93.184.216.34")
		if rrErr == nil {
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})

	srv := &dns.Server{PacketConn: pc, Handler: mux}
	ready := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() { _ = srv.ActivateAndServe() }()
	<-ready

	return pc, srv
}

// startConnectStub runs a raw CONNECT proxy. It answers 200, sends the
// request line on lines, then echoes every byte it reads back to the
// caller.
func startConnectStub(t *testing.T, lines chan<- string) net.Listener {
	t.Helper()

	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go serveConnectStub(conn, lines)
		}
	}()

	return ln
}

func serveConnectStub(conn net.Conn, lines chan<- string) {
	defer func() { _ = conn.Close() }()

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	lines <- strings.TrimRight(line, "\r\n")

	for {
		h, hErr := br.ReadString('\n')
		if hErr != nil || h == "\r\n" {
			break
		}
	}

	if _, err = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	_, _ = io.Copy(conn, br)
}

// TestDNSAnswersUnderTheDomainWithSelf proves that a name under the domain
// resolves to the relay's configured self address.
func (s *RelayProcessSuite) TestDNSAnswersUnderTheDomainWithSelf() {
	client := dns.Client{Timeout: 2 * time.Second}
	req := new(dns.Msg)
	req.SetQuestion("web."+relayTestDomain+".", dns.TypeA)

	reply, _, err := client.Exchange(req, s.proc.dnsAddr())
	s.Require().NoError(err)

	s.Require().Len(reply.Answer, 1, "an A query under the domain must get one answer")
	a, ok := reply.Answer[0].(*dns.A)
	s.Require().True(ok, "the answer must be an A record")
	s.Equal("10.20.30.40", a.A.String())
}

// TestDNSForwardsOutsideTheDomain proves that a name outside the domain
// reaches the upstream resolver.
func (s *RelayProcessSuite) TestDNSForwardsOutsideTheDomain() {
	client := dns.Client{Timeout: 2 * time.Second}
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)

	reply, _, err := client.Exchange(req, s.proc.dnsAddr())
	s.Require().NoError(err)

	s.Require().Len(reply.Answer, 1)
	a, ok := reply.Answer[0].(*dns.A)
	s.Require().True(ok)
	s.Equal("93.184.216.34", a.A.String())
}

// TestDNSAAAAAnswersEmptyNotNXDOMAIN proves that AAAA under the domain gets
// an empty NOERROR answer, so a dual-stack client falls back to A.
func (s *RelayProcessSuite) TestDNSAAAAAnswersEmptyNotNXDOMAIN() {
	client := dns.Client{Timeout: 2 * time.Second}
	req := new(dns.Msg)
	req.SetQuestion("web."+relayTestDomain+".", dns.TypeAAAA)

	reply, _, err := client.Exchange(req, s.proc.dnsAddr())
	s.Require().NoError(err)

	s.Empty(reply.Answer, "an AAAA query must get no answer, not an error")
	s.Equal(dns.RcodeSuccess, reply.Rcode, "an empty AAAA must answer NOERROR, not NXDOMAIN")
}

// TestHTTPForwarderSendsAbsoluteURI proves that the relay forwards an HTTP
// request to the proxy in absolute-URI proxy form.
func (s *RelayProcessSuite) TestHTTPForwarderSendsAbsoluteURI() {
	req, err := http.NewRequestWithContext(s.T().Context(), http.MethodGet,
		"http://"+s.proc.httpAddr()+"/path", nil)
	s.Require().NoError(err)
	req.Host = "web." + relayTestDomain

	resp, err := http.DefaultClient.Do(req)
	s.Require().NoError(err)
	defer func() { _ = resp.Body.Close() }()

	select {
	case line := <-s.proxyLines:
		s.Equal("GET http://web."+relayTestDomain+"/path HTTP/1.1", line,
			"a workload must reach a step without proxy variables, and the proxy sees the absolute-URI form")
	case <-time.After(2 * time.Second):
		s.Fail("the stub proxy never saw a request")
	}
}

// TestSOCKS5ForwardsToTarget proves that the relay's SOCKS5 gateway CONNECTs
// to whatever target address a client asks for, the same path a
// builtin:container step's relay: true expose entry relies on.
func (s *RelayProcessSuite) TestSOCKS5ForwardsToTarget() {
	t := s.T()

	var lc net.ListenConfig
	echoLn, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	s.Require().NoError(err)
	defer func() { _ = echoLn.Close() }()
	go func() {
		conn, acceptErr := echoLn.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn)
	}()

	dialer, err := proxy.SOCKS5("tcp", s.proc.socks5Addr(), nil, proxy.Direct)
	s.Require().NoError(err)

	conn, err := dialer.Dial("tcp", echoLn.Addr().String())
	s.Require().NoError(err)
	defer func() { _ = conn.Close() }()

	_, err = conn.Write([]byte("hello"))
	s.Require().NoError(err)

	buf := make([]byte, 5)
	_, err = io.ReadFull(conn, buf)
	s.Require().NoError(err)
	s.Equal("hello", string(buf))
}

// TestResolveSelfFindsARealInterfaceAddress proves that resolveSelf, which
// serve calls when a caller leaves -self empty, finds a usable address on
// the test host.
func (s *RelayProcessSuite) TestResolveSelfFindsARealInterfaceAddress() {
	addr, err := resolveSelf()
	s.Require().NoError(err)
	s.NotEmpty(addr)
}

// recordingConn wraps a net.Conn and keeps every byte that Read returns.
type recordingConn struct {
	net.Conn

	mu  sync.Mutex
	buf []byte
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.mu.Lock()
		c.buf = append(c.buf, p[:n]...)
		c.mu.Unlock()
	}
	return n, err
}

func (c *recordingConn) snapshot() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buf...)
}

// TestHTTPSForwarderRelaysTheClientHello proves that the relay opens a
// CONNECT tunnel for the SNI host, and replays the ClientHello bytes it
// already consumed. The HTTP forwarder and the HTTPS forwarder share one
// proxyAddr, so this test runs its own relay process against a CONNECT
// stub, separate from the suite's shared process.
func (s *RelayProcessSuite) TestHTTPSForwarderRelaysTheClientHello() {
	t := s.T()

	connectLine := make(chan string, 1)
	connectLn := startConnectStub(t, connectLine)
	defer func() { _ = connectLn.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	proc, err := newRelayProcess(ctx, config{
		domain:       relayTestDomain,
		proxyAddr:    connectLn.Addr().String(),
		self:         "10.20.30.40",
		dnsListen:    "127.0.0.1:0",
		httpListen:   "127.0.0.1:0",
		httpsListen:  "127.0.0.1:0",
		socks5Listen: "127.0.0.1:0",
		upstreamDNS:  s.upstreamPC.LocalAddr().String(),
	})
	s.Require().NoError(err)

	done := make(chan error, 1)
	go func() { done <- proc.run(ctx) }()

	var d net.Dialer
	raw, err := d.DialContext(t.Context(), "tcp", proc.httpsAddr())
	s.Require().NoError(err)
	rec := &recordingConn{Conn: raw}

	sni := "web." + relayTestDomain
	tlsConn := tls.Client(rec, &tls.Config{ServerName: sni, InsecureSkipVerify: true})

	handshakeDone := make(chan struct{})
	go func() {
		_ = tlsConn.HandshakeContext(t.Context())
		close(handshakeDone)
	}()

	select {
	case line := <-connectLine:
		s.Equal("CONNECT "+sni+":443 HTTP/1.1", line)
	case <-time.After(2 * time.Second):
		s.Fail("the stub proxy never saw a CONNECT request")
	}

	s.Require().Eventually(func() bool { return len(rec.snapshot()) > 0 }, 2*time.Second, 10*time.Millisecond,
		"the echo must carry the client hello back to the caller")

	echoed := rec.snapshot()
	s.Equal(byte(0x16), echoed[0], "the echoed record must start with the tls handshake record type")

	_ = raw.Close()
	<-handshakeDone
	cancel()
	s.Require().NoError(<-done)
}
